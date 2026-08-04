package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNonStreamChatDoesNotUseFirstByteTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(120 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	gw := NewGateway(sched, nil, nil)
	result := gw.executeOpenAINonStream(context.Background(), []byte(`{"model":"meta/llama-3.1-8b-instruct","messages":[{"role":"user","content":"hello"}]}`), "meta/llama-3.1-8b-instruct", "hello", nil, 0, nil, "timeout:test")
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", result.StatusCode, string(result.Body))
	}
}
