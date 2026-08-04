package gateway

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolChoiceFunctionObjectFallsBackToAuto(t *testing.T) {
	value, ok := normalizeToolChoice(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "lookup_weather",
		},
	})
	if !ok {
		t.Fatal("expected tool choice to be normalized")
	}
	if value != "auto" {
		t.Fatalf("expected auto fallback, got %#v", value)
	}
}

func TestTranslateRequestSanitizesToolChoiceObject(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"lookup_weather"}}}`)
	translated, _, _, _, err := TranslateRequest(body)
	if err != nil {
		t.Fatalf("TranslateRequest failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatalf("unmarshal translated failed: %v", err)
	}
	if payload["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice=auto, got %#v", payload["tool_choice"])
	}
}

func TestTranslateResponsesRequestSanitizesToolChoiceObject(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hi","tool_choice":{"type":"function","function":{"name":"lookup_weather"}}}`)
	translated, _, err := TranslateResponsesRequest(body)
	if err != nil {
		t.Fatalf("TranslateResponsesRequest failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatalf("unmarshal translated failed: %v", err)
	}
	if payload["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice=auto, got %#v", payload["tool_choice"])
	}
}
