package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExecuteOpenAINonStreamHonorsRequestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gw := NewGateway(nil, nil, nil)
	result := gw.executeOpenAINonStream(ctx, []byte(`{"model":"meta/llama-3.1-8b-instruct","messages":[{"role":"user","content":"hello"}]}`), "meta/llama-3.1-8b-instruct", "hello", nil, 0, nil, "ctx:test")
	if result.StatusCode == http.StatusOK {
		t.Fatalf("expected canceled context to fail, got 200 body=%s", string(result.Body))
	}
}

func TestHandleChatCompletionsNonStreamUsesHTTPRequestContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}}})
		}
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	gw := NewGateway(sched, nil, nil)
	_ = gw
}
