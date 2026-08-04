package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"

	"github.com/gofiber/fiber/v2"
)

func TestUpdateSystemConfigPrefersProxyIDOverEmptyURL(t *testing.T) {
	prepareGatewayTestState(t, "https://integrate.api.nvidia.com/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	if err := db.UpdateStore(func(store *db.Store) error {
		store.Proxies = []models.UpstreamProxy{{
			ID:        2050,
			Name:      "[XRAY] fast",
			ManagedBy: models.CoreManagedByXray,
			Type:      "socks5h",
			Status:    models.ProxyStatusEnabled,
			Host:      "127.0.0.1",
			Port:      21036,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}}
		store.NextProxyID = 2051
		return nil
	}); err != nil {
		t.Fatalf("seed proxies: %v", err)
	}
	app := fiber.New()
	app.Put("/admin/system", UpdateSystemConfig(scheduler.NewScheduler(nil)))
	req := httptest.NewRequest(http.MethodPut, "/admin/system", strings.NewReader(`{"upstreamProxyId":2050,"upstreamProxyURL":""}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("system config request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		t.Fatalf("expected 200, got %d payload=%v", resp.StatusCode, payload)
	}
	store, err := db.ReadStore()
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if store.SystemConfig.UpstreamProxyID != 2050 {
		t.Fatalf("upstream proxy id = %d, want 2050", store.SystemConfig.UpstreamProxyID)
	}
	if strings.TrimSpace(store.SystemConfig.UpstreamProxyURL) != "" {
		t.Fatalf("upstream proxy url = %q, want empty", store.SystemConfig.UpstreamProxyURL)
	}
}
