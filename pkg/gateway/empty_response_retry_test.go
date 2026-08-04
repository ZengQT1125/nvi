package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNonStreamEmptyResponseRetriesUntilContent(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		hit := atomic.AddInt32(&hits, 1)
		if hit == 1 {
			writeJSON(w, http.StatusOK, map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"role": "assistant", "content": ""},
				}},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "recovered"},
			}},
		})
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	gw := NewGateway(sched, nil, nil)
	result := gw.executeOpenAINonStream(context.Background(), []byte(`{"model":"meta/llama-3.1-8b-instruct","messages":[{"role":"user","content":"hello"}]}`), "meta/llama-3.1-8b-instruct", "hello", nil, 0, nil, "empty:nonstream")
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", result.StatusCode, string(result.Body))
	}
	if !strings.Contains(string(result.Body), "recovered") {
		t.Fatalf("expected recovered body, got %s", string(result.Body))
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected retry after empty response, hits=%d", hits)
	}
}

func TestStreamEmptyResponseRetriesUntilContent(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		hit := atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		if hit == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"stream-recovered\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	gw := NewGateway(sched, nil, nil)
	writer := newCaptureWriter()
	gw.executeOpenAIStream(context.Background(), writer, []byte(`{"model":"meta/llama-3.1-8b-instruct","messages":[{"role":"user","content":"hello"}],"stream":true}`), "meta/llama-3.1-8b-instruct", "hello", nil, 0, nil, "empty:stream")
	if writer.status != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", writer.status, writer.body.String())
	}
	if !strings.Contains(writer.body.String(), "stream-recovered") {
		t.Fatalf("expected recovered stream, got %s", writer.body.String())
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected retry after empty stream, hits=%d", hits)
	}
}
