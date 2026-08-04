package gateway

import (
	"encoding/base64"
	"net/url"
	"testing"
)

func TestNormalizeSubscriptionPayloadDecodesWrappedBase64(t *testing.T) {
	link := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=reality&sni=www.example.com&pbk=pubkey&sid=abcd&type=tcp#Example"
	wrapped := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(link)))
	got := normalizeSubscriptionPayload(wrapped)
	if got != link {
		t.Fatalf("normalizeSubscriptionPayload() = %q, want %q", got, link)
	}
}

func TestParseCoreImportRequestsHandlesHTMLEscapedLinks(t *testing.T) {
	raw := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&amp;security=reality&amp;sni=www.tesla.com&amp;fp=chrome&amp;pbk=pubkey&amp;sid=abcd&amp;type=tcp#Tesla"
	items, warnings, err := parseCoreImportRequests(raw)
	if err != nil {
		t.Fatalf("parseCoreImportRequests() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if item.Protocol != "vless" || item.TLSMode != "reality" || item.SNI != "www.tesla.com" || item.RealityPublicKey != "pubkey" {
		t.Fatalf("unexpected parsed item: %+v", item)
	}
}
