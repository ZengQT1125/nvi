package gateway

import (
	"testing"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
)

func TestUpsertImportedFreeProxyKeepsManualEntry(t *testing.T) {
	now := time.Unix(200, 0)
	store := &db.Store{
		NextProxyID: 9,
		Proxies: []models.UpstreamProxy{{
			ID:        7,
			Name:      "手动代理",
			Group:     "手动",
			Source:    models.ProxySourceManual,
			Type:      "http",
			Status:    models.ProxyStatusDisabled,
			Host:      "1.2.3.4",
			Port:      8080,
			CreatedAt: time.Unix(100, 0),
			UpdatedAt: time.Unix(101, 0),
		}},
	}
	summary := freeProxyImportSummary{}
	item := testedFreeProxy{
		Candidate: freeProxyCandidate{Type: "http", Host: "1.2.3.4", Port: 8080},
		Record:    models.ProxyTestRecord{Success: true, StatusCode: 204, ResponseTime: 123, TestedAt: now},
		Success:   true,
	}

	upsertImportedFreeProxy(store, item, "自动抓取", now, &summary)

	if len(store.Proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(store.Proxies))
	}
	proxy := models.NormalizeUpstreamProxy(store.Proxies[0])
	if proxy.Name != "手动代理" {
		t.Fatalf("manual proxy name changed: %q", proxy.Name)
	}
	if proxy.Source != models.ProxySourceManual {
		t.Fatalf("manual proxy source = %q, want %q", proxy.Source, models.ProxySourceManual)
	}
	if proxy.Status != models.ProxyStatusDisabled {
		t.Fatalf("manual proxy status changed: %q", proxy.Status)
	}
	if proxy.LastTest == nil || !proxy.LastTest.Success {
		t.Fatalf("manual proxy last test not updated: %#v", proxy.LastTest)
	}
	if summary.MatchedManualCount != 1 || summary.ImportedCount != 0 || summary.UpdatedCount != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestUpsertImportedFreeProxyRefreshesAutoEntry(t *testing.T) {
	now := time.Unix(300, 0)
	store := &db.Store{
		NextProxyID: 11,
		Proxies: []models.UpstreamProxy{{
			ID:          10,
			Name:        "AUTO http://9.9.9.9:3128",
			Group:       "旧分组",
			Source:      models.ProxySourceAuto,
			Type:        "http",
			Status:      models.ProxyStatusDisabled,
			Host:        "9.9.9.9",
			Port:        3128,
			TestHistory: []models.ProxyTestRecord{{Success: false, StatusCode: 500, TestedAt: time.Unix(200, 0)}},
			CreatedAt:   time.Unix(100, 0),
			UpdatedAt:   time.Unix(101, 0),
		}},
	}
	summary := freeProxyImportSummary{}
	item := testedFreeProxy{
		Candidate: freeProxyCandidate{Type: "http", Host: "9.9.9.9", Port: 3128, Country: "SG"},
		Record:    models.ProxyTestRecord{Success: true, StatusCode: 204, ResponseTime: 88, TestedAt: now},
		Success:   true,
	}

	upsertImportedFreeProxy(store, item, "自动抓取", now, &summary)

	if len(store.Proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(store.Proxies))
	}
	proxy := models.NormalizeUpstreamProxy(store.Proxies[0])
	if proxy.Status != models.ProxyStatusEnabled {
		t.Fatalf("auto proxy status = %q, want %q", proxy.Status, models.ProxyStatusEnabled)
	}
	if proxy.Group != "旧分组" {
		t.Fatalf("auto proxy group changed: %q", proxy.Group)
	}
	if proxy.LastTest == nil || proxy.LastTest.ResponseTime != 88 {
		t.Fatalf("auto proxy last test not refreshed: %#v", proxy.LastTest)
	}
	if proxy.Country != "SG" {
		t.Fatalf("auto proxy country = %q, want SG", proxy.Country)
	}
	if len(proxy.TestHistory) < 2 {
		t.Fatalf("auto proxy history not appended: %#v", proxy.TestHistory)
	}
	if summary.UpdatedCount != 1 || summary.ImportedCount != 0 || summary.MatchedManualCount != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestUpsertImportedFreeProxyAppendsNewAutoEntry(t *testing.T) {
	now := time.Unix(400, 0)
	store := &db.Store{
		NextProxyID: 3,
		Proxies:     []models.UpstreamProxy{},
	}
	summary := freeProxyImportSummary{}
	item := testedFreeProxy{
		Candidate: freeProxyCandidate{Type: "socks5h", Host: "8.8.8.8", Port: 1080, Country: "US"},
		Record:    models.ProxyTestRecord{Success: true, StatusCode: 204, ResponseTime: 66, TestedAt: now},
		Success:   true,
	}

	upsertImportedFreeProxy(store, item, "自动抓取", now, &summary)

	if len(store.Proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(store.Proxies))
	}
	proxy := models.NormalizeUpstreamProxy(store.Proxies[0])
	if proxy.ID != 3 {
		t.Fatalf("proxy id = %d, want 3", proxy.ID)
	}
	if store.NextProxyID != 4 {
		t.Fatalf("next proxy id = %d, want 4", store.NextProxyID)
	}
	if proxy.Source != models.ProxySourceAuto {
		t.Fatalf("proxy source = %q, want %q", proxy.Source, models.ProxySourceAuto)
	}
	if proxy.Group != "自动抓取" {
		t.Fatalf("proxy group = %q, want 自动抓取", proxy.Group)
	}
	if proxy.Name == "" || proxy.LastTest == nil || !proxy.LastTest.Success {
		t.Fatalf("auto proxy not initialized correctly: %#v", proxy)
	}
	if summary.ImportedCount != 1 || summary.UpdatedCount != 0 || summary.MatchedManualCount != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}
