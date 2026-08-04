package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTranslateResponsesRequestFromStringInput(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hello responses","stream":true,"max_output_tokens":256}`)
	translated, meta, err := TranslateResponsesRequest(body)
	if err != nil {
		t.Fatalf("TranslateResponsesRequest failed: %v", err)
	}
	if meta == nil || !meta.Stream || meta.RequestedModel != "gpt-4o" {
		t.Fatalf("unexpected meta: %#v", meta)
	}
	var payload map[string]any
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatalf("unmarshal translated failed: %v", err)
	}
	if payload["model"] != normalizeModel("gpt-4o") {
		t.Fatalf("unexpected model: %#v", payload["model"])
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one translated message, got %#v", payload["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hello responses" {
		t.Fatalf("unexpected translated message: %#v", msg)
	}
	if payload["max_tokens"] != float64(256) {
		t.Fatalf("expected max_tokens=256, got %#v", payload["max_tokens"])
	}
}

func TestTranslateResponsesRequestWithPreviousResponseIDRehydratesConversation(t *testing.T) {
	originalStore := responsesStore
	responsesStore = newGatewayResponseStore(30 * time.Minute)
	defer func() { responsesStore = originalStore }()

	responsesStore.put("resp_prev", []byte(`{"id":"resp_prev"}`), []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi there"},
	})

	body := []byte(`{"model":"gpt-4o","previous_response_id":"resp_prev","input":"continue please"}`)
	translated, meta, err := TranslateResponsesRequest(body)
	if err != nil {
		t.Fatalf("TranslateResponsesRequest failed: %v", err)
	}
	if meta == nil || len(meta.Messages) != 3 {
		t.Fatalf("unexpected meta messages: %#v", meta)
	}
	var payload map[string]any
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatalf("unmarshal translated failed: %v", err)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("expected three translated messages, got %#v", payload["messages"])
	}
	got := make([]string, 0, len(messages))
	for _, item := range messages {
		msg, _ := item.(map[string]any)
		got = append(got, stringValue(msg["content"]))
	}
	if strings.Join(got, "|") != "hello|hi there|continue please" {
		t.Fatalf("unexpected replayed conversation: %#v", got)
	}
}

func TestTranslateResponsesRequestFunctionCallOutputPreservesToolMessage(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":[{"type":"function_call_output","call_id":"call_1","output":{"city":"Shanghai","temp":28}}]}`)
	translated, _, err := TranslateResponsesRequest(body)
	if err != nil {
		t.Fatalf("TranslateResponsesRequest failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatalf("unmarshal translated failed: %v", err)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one translated message, got %#v", payload["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "tool" {
		t.Fatalf("expected tool role, got %#v", msg)
	}
	if msg["tool_call_id"] != "call_1" {
		t.Fatalf("expected tool_call_id=call_1, got %#v", msg)
	}
	if msg["content"] != `{"city":"Shanghai","temp":28}` {
		t.Fatalf("expected JSON-stringified tool output, got %#v", msg)
	}
}

func TestBuildResponsesObjectFromOpenAIResult(t *testing.T) {
	raw := mustMarshalTestJSON(map[string]any{
		"id":    "chatcmpl_test",
		"model": "meta/llama-3.1-70b-instruct",
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message": map[string]any{
				"role":    "assistant",
				"content": "hello response object",
			},
		}},
		"usage": map[string]any{
			"prompt_tokens":     12,
			"completion_tokens": 8,
			"total_tokens":      20,
		},
	})
	payload, err := buildResponsesObjectFromOpenAIResult("resp_test", "gpt-4o", raw)
	if err != nil {
		t.Fatalf("buildResponsesObjectFromOpenAIResult failed: %v", err)
	}
	text := string(payload)
	for _, needle := range []string{`"id":"resp_test"`, `"object":"response"`, `"output_text":"hello response object"`, `"input_tokens":12`, `"output_tokens":8`} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected payload to contain %q, got %s", needle, text)
		}
	}
}
