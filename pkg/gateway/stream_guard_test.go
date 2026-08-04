package gateway

import (
	"strings"
	"testing"
)

func TestInspectOpenAIStreamPrefetchTreatsRoleOnlyDoneAsEmpty(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: [DONE]\n\n"
	meaningful, done := inspectOpenAIStreamPrefetch([]byte(stream))
	if meaningful {
		t.Fatal("expected role-only stream to be treated as empty")
	}
	if !done {
		t.Fatal("expected [DONE] to be detected")
	}
}

func TestInspectOpenAIStreamPrefetchDetectsContent(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	meaningful, done := inspectOpenAIStreamPrefetch([]byte(stream))
	if !meaningful {
		t.Fatal("expected content stream to be meaningful")
	}
	if done {
		t.Fatal("did not expect done before [DONE]")
	}
}

func TestIsOpenAIChatCompletionEmpty(t *testing.T) {
	if !isOpenAIChatCompletionEmpty([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)) {
		t.Fatal("expected empty message to be empty")
	}
	if isOpenAIChatCompletionEmpty([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)) {
		t.Fatal("expected content message to be non-empty")
	}
	if isOpenAIChatCompletionEmpty([]byte(`{"error":{"message":"bad"}}`)) {
		t.Fatal("expected upstream error payload not to be hidden as empty")
	}
}

func TestOpenAIStreamBytesContainDone(t *testing.T) {
	if !openAIStreamBytesContainDone([]byte(strings.ReplaceAll("data: [DONE]\n\n", "\n", "\r\n"))) {
		t.Fatal("expected done marker to be detected")
	}
}
