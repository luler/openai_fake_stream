package logic

import (
	"bytes"
	"encoding/json"
	"sort"

	"gin_base/app/helper/sse_helper"
)

// 将上游的非流式 JSON 响应转换为流式 SSE 输出
// 支持：chat.completion（/v1/chat/completions）、text_completion（/v1/completions）、response（/v1/responses）
// 其他未知结构：整体作为一个 data 事件下发，再补一个 [DONE]
func StreamJSON(w *sse_helper.SSEWriter, body []byte, chunkSize int, includeUsage bool) {
	obj, ok := decodeObject(body)
	if !ok {
		w.DataRaw(body)
		w.Done()
		return
	}
	switch detectKind(obj) {
	case "chat":
		streamChatCompletion(w, obj, chunkSize, includeUsage)
	case "text":
		streamTextCompletion(w, obj, chunkSize, includeUsage)
	case "response":
		streamResponses(w, obj, chunkSize)
	default:
		w.DataRaw(body)
		w.Done()
	}
}

// 已开始 SSE 输出后才拿到上游错误时，以流的形式下发错误
func StreamError(w *sse_helper.SSEWriter, responsesAPI bool, statusCode int, body []byte) {
	obj, ok := decodeObject(body)
	if !ok || obj["error"] == nil {
		obj = map[string]any{"error": map[string]any{
			"message": string(body),
			"type":    "upstream_error",
			"code":    statusCode,
		}}
	}
	if responsesAPI {
		errObj, _ := obj["error"].(map[string]any)
		ev := map[string]any{"type": "error", "code": nil, "message": string(body), "param": nil, "sequence_number": 0}
		if errObj != nil {
			ev["code"], ev["message"], ev["param"] = errObj["code"], errObj["message"], errObj["param"]
		}
		w.Event("error", ev)
		return
	}
	w.Data(obj)
	w.Done()
}

func decodeObject(body []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 保持数字原样
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func detectKind(obj map[string]any) string {
	switch obj["object"] {
	case "chat.completion":
		return "chat"
	case "text_completion":
		return "text"
	case "response":
		return "response"
	}
	// 部分兼容接口不返回 object 字段，按结构推断
	if choices, ok := obj["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if _, ok := c["message"]; ok {
				return "chat"
			}
			if _, ok := c["text"]; ok {
				return "text"
			}
		}
	}
	if _, ok := obj["output"].([]any); ok {
		return "response"
	}
	return ""
}

// 按 rune 切分文本
func splitText(s string, size int) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	if size <= 0 || len(runes) <= size {
		return []string{s}
	}
	parts := make([]string, 0, len(runes)/size+1)
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[i:end]))
	}
	return parts
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---------------- /v1/chat/completions ----------------

func streamChatCompletion(w *sse_helper.SSEWriter, resp map[string]any, chunkSize int, includeUsage bool) {
	newChunk := func(choices []any) map[string]any {
		chunk := map[string]any{
			"id":      resp["id"],
			"object":  "chat.completion.chunk",
			"created": resp["created"],
			"model":   resp["model"],
			"choices": choices,
		}
		for _, k := range []string{"system_fingerprint", "service_tier"} {
			if v, ok := resp[k]; ok {
				chunk[k] = v
			}
		}
		return chunk
	}
	emit := func(index any, delta map[string]any, finishReason any, logprobs any) {
		w.Data(newChunk([]any{map[string]any{
			"index":         index,
			"delta":         delta,
			"logprobs":      logprobs,
			"finish_reason": finishReason,
		}}))
	}

	handled := map[string]bool{
		"role": true, "content": true, "reasoning_content": true, "reasoning": true,
		"tool_calls": true, "function_call": true,
	}

	choices, _ := resp["choices"].([]any)
	for i, item := range choices {
		choice, _ := item.(map[string]any)
		if choice == nil {
			continue
		}
		index := choice["index"]
		if index == nil {
			index = i
		}
		msg, _ := choice["message"].(map[string]any)
		if msg == nil {
			msg = map[string]any{}
		}

		// 1. 角色
		role := msg["role"]
		if role == nil {
			role = "assistant"
		}
		emit(index, map[string]any{"role": role, "content": ""}, nil, nil)

		// 2. 思考内容（DeepSeek 等使用 reasoning_content，部分厂商使用 reasoning）
		for _, key := range []string{"reasoning_content", "reasoning"} {
			if s, ok := msg[key].(string); ok {
				for _, p := range splitText(s, chunkSize) {
					emit(index, map[string]any{key: p}, nil, nil)
				}
			}
		}

		// 3. 正文
		switch content := msg["content"].(type) {
		case nil:
		case string:
			for _, p := range splitText(content, chunkSize) {
				emit(index, map[string]any{"content": p}, nil, nil)
			}
		default:
			emit(index, map[string]any{"content": content}, nil, nil)
		}

		// 4. 工具调用：先下发 id/name，再分片下发 arguments
		if toolCalls, ok := msg["tool_calls"].([]any); ok {
			for ti, t := range toolCalls {
				tc, _ := t.(map[string]any)
				if tc == nil {
					continue
				}
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					head := shallowCopy(tc)
					head["index"] = ti
					emit(index, map[string]any{"tool_calls": []any{head}}, nil, nil)
					continue
				}
				typ := tc["type"]
				if typ == nil {
					typ = "function"
				}
				emit(index, map[string]any{"tool_calls": []any{map[string]any{
					"index":    ti,
					"id":       tc["id"],
					"type":     typ,
					"function": map[string]any{"name": fn["name"], "arguments": ""},
				}}}, nil, nil)
				args, _ := fn["arguments"].(string)
				for _, p := range splitText(args, chunkSize) {
					emit(index, map[string]any{"tool_calls": []any{map[string]any{
						"index":    ti,
						"function": map[string]any{"arguments": p},
					}}}, nil, nil)
				}
			}
		}

		// 旧版 function_call
		if fc, ok := msg["function_call"].(map[string]any); ok {
			emit(index, map[string]any{"function_call": map[string]any{"name": fc["name"], "arguments": ""}}, nil, nil)
			args, _ := fc["arguments"].(string)
			for _, p := range splitText(args, chunkSize) {
				emit(index, map[string]any{"function_call": map[string]any{"arguments": p}}, nil, nil)
			}
		}

		// 5. 其他字段（refusal、audio、annotations 等）原样各下发一次
		keys := make([]string, 0, len(msg))
		for k, v := range msg {
			if !handled[k] && v != nil {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			emit(index, map[string]any{k: msg[k]}, nil, nil)
		}

		// 6. 结束
		emit(index, map[string]any{}, choice["finish_reason"], choice["logprobs"])
	}

	// 7. usage（仅在客户端请求 stream_options.include_usage 时下发，与 OpenAI 行为一致）
	if includeUsage && resp["usage"] != nil {
		chunk := newChunk([]any{})
		chunk["usage"] = resp["usage"]
		w.Data(chunk)
	}
	w.Done()
}

// ---------------- /v1/completions ----------------

func streamTextCompletion(w *sse_helper.SSEWriter, resp map[string]any, chunkSize int, includeUsage bool) {
	newChunk := func(choices []any) map[string]any {
		chunk := map[string]any{
			"id":      resp["id"],
			"object":  "text_completion",
			"created": resp["created"],
			"model":   resp["model"],
			"choices": choices,
		}
		if v, ok := resp["system_fingerprint"]; ok {
			chunk["system_fingerprint"] = v
		}
		return chunk
	}
	choices, _ := resp["choices"].([]any)
	for i, item := range choices {
		choice, _ := item.(map[string]any)
		if choice == nil {
			continue
		}
		index := choice["index"]
		if index == nil {
			index = i
		}
		text, _ := choice["text"].(string)
		for _, p := range splitText(text, chunkSize) {
			w.Data(newChunk([]any{map[string]any{"text": p, "index": index, "logprobs": nil, "finish_reason": nil}}))
		}
		w.Data(newChunk([]any{map[string]any{"text": "", "index": index, "logprobs": choice["logprobs"], "finish_reason": choice["finish_reason"]}}))
	}
	if includeUsage && resp["usage"] != nil {
		chunk := newChunk([]any{})
		chunk["usage"] = resp["usage"]
		w.Data(chunk)
	}
	w.Done()
}

// ---------------- /v1/responses ----------------

func streamResponses(w *sse_helper.SSEWriter, resp map[string]any, chunkSize int) {
	seq := 0
	send := func(typ string, payload map[string]any) {
		payload["type"] = typ
		payload["sequence_number"] = seq
		seq++
		w.Event(typ, payload)
	}

	// 初始快照：进行中、无输出
	snapshot := shallowCopy(resp)
	snapshot["status"] = "in_progress"
	snapshot["output"] = []any{}
	snapshot["usage"] = nil
	delete(snapshot, "output_text")
	if _, ok := snapshot["completed_at"]; ok {
		snapshot["completed_at"] = nil
	}
	send("response.created", map[string]any{"response": snapshot})
	send("response.in_progress", map[string]any{"response": snapshot})

	output, _ := resp["output"].([]any)
	for oi, it := range output {
		item, _ := it.(map[string]any)
		if item == nil {
			continue
		}
		itemID := item["id"]

		switch item["type"] {
		case "message":
			added := shallowCopy(item)
			added["status"] = "in_progress"
			added["content"] = []any{}
			send("response.output_item.added", map[string]any{"output_index": oi, "item": added})

			parts, _ := item["content"].([]any)
			for ci, pt := range parts {
				part, _ := pt.(map[string]any)
				if part == nil {
					continue
				}
				base := func() map[string]any {
					return map[string]any{"item_id": itemID, "output_index": oi, "content_index": ci}
				}
				switch part["type"] {
				case "output_text":
					text, _ := part["text"].(string)
					empty := shallowCopy(part)
					empty["text"] = ""
					empty["annotations"] = []any{}
					ev := base()
					ev["part"] = empty
					send("response.content_part.added", ev)
					for _, p := range splitText(text, chunkSize) {
						ev := base()
						ev["delta"] = p
						ev["logprobs"] = []any{}
						send("response.output_text.delta", ev)
					}
					ev = base()
					ev["text"] = text
					ev["logprobs"] = []any{}
					send("response.output_text.done", ev)
				case "refusal":
					refusal, _ := part["refusal"].(string)
					empty := shallowCopy(part)
					empty["refusal"] = ""
					ev := base()
					ev["part"] = empty
					send("response.content_part.added", ev)
					for _, p := range splitText(refusal, chunkSize) {
						ev := base()
						ev["delta"] = p
						send("response.refusal.delta", ev)
					}
					ev = base()
					ev["refusal"] = refusal
					send("response.refusal.done", ev)
				default:
					ev := base()
					ev["part"] = part
					send("response.content_part.added", ev)
				}
				ev := base()
				ev["part"] = part
				send("response.content_part.done", ev)
			}

		case "function_call":
			args, _ := item["arguments"].(string)
			added := shallowCopy(item)
			added["arguments"] = ""
			added["status"] = "in_progress"
			send("response.output_item.added", map[string]any{"output_index": oi, "item": added})
			for _, p := range splitText(args, chunkSize) {
				send("response.function_call_arguments.delta", map[string]any{
					"item_id": itemID, "output_index": oi, "delta": p,
				})
			}
			send("response.function_call_arguments.done", map[string]any{
				"item_id": itemID, "output_index": oi, "arguments": args, "name": item["name"],
			})

		case "reasoning":
			added := shallowCopy(item)
			added["summary"] = []any{}
			send("response.output_item.added", map[string]any{"output_index": oi, "item": added})

			summaries, _ := item["summary"].([]any)
			for si, s := range summaries {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				text, _ := sm["text"].(string)
				base := func() map[string]any {
					return map[string]any{"item_id": itemID, "output_index": oi, "summary_index": si}
				}
				ev := base()
				ev["part"] = map[string]any{"type": "summary_text", "text": ""}
				send("response.reasoning_summary_part.added", ev)
				for _, p := range splitText(text, chunkSize) {
					ev := base()
					ev["delta"] = p
					send("response.reasoning_summary_text.delta", ev)
				}
				ev = base()
				ev["text"] = text
				send("response.reasoning_summary_text.done", ev)
				ev = base()
				ev["part"] = map[string]any{"type": "summary_text", "text": text}
				send("response.reasoning_summary_part.done", ev)
			}

		default:
			send("response.output_item.added", map[string]any{"output_index": oi, "item": item})
		}

		send("response.output_item.done", map[string]any{"output_index": oi, "item": item})
	}

	final := "response.completed"
	switch resp["status"] {
	case "incomplete":
		final = "response.incomplete"
	case "failed":
		final = "response.failed"
	}
	send(final, map[string]any{"response": resp})
}
