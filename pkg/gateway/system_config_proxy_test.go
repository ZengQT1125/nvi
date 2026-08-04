package gateway

import (
	"context"
	"os"
	"testing"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"
)

func TestSelectAlternateManagedProxyURLForAPIKey(t *testing.T) {
	resetUpstreamRuntimeStateForTest()
	defer resetUpstreamRuntimeStateForTest()
	old := os.Getenv("ENCRYPTION_KEY")
	_ = os.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	defer func() { _ = os.Setenv("ENCRYPTION_KEY", old) }()
	storePath := t.TempDir()
	db.InitDB(storePath)
	enc, err := utils.Encrypt("nvapi-test", testEncryptionKey)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	if err := db.UpdateStore(func(store *db.Store) error {
		store.APIKeys = []models.APIKey{{ID: 1, Name: "NVIDIA-01", Key: enc, Status: APIKeyStatusActive, ProxyID: 31, CreatedAt: time.Now(), UpdatedAt: time.Now()}}
		store.Proxies = []models.UpstreamProxy{
			{ID: 31, Name: "bad", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Status: models.ProxyStatusEnabled, Host: "127.0.0.1", Port: 21031, LastTest: &models.ProxyTestRecord{Success: false}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: 36, Name: "good-fast", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Status: models.ProxyStatusEnabled, Host: "127.0.0.1", Port: 21036, LastTest: &models.ProxyTestRecord{Success: true, ResponseTime: 100}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: 34, Name: "good-slow", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Status: models.ProxyStatusEnabled, Host: "127.0.0.1", Port: 21034, LastTest: &models.ProxyTestRecord{Success: true, ResponseTime: 500}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		}
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}
	if err := rebuildUpstreamRuntime(mustReadStoreForTest(t)); err != nil {
		t.Fatalf("rebuild runtime: %v", err)
	}
	alt, ok := selectAlternateManagedProxyURLForAPIKey(models.DefaultSystemConfig(), "nvapi-test", "socks5h://127.0.0.1:21031")
	if !ok {
		t.Fatal("expected alternate proxy")
	}
	if alt != "socks5h://127.0.0.1:21036" {
		t.Fatalf("alternate = %q", alt)
	}
}

func mustReadStoreForTest(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.ReadStore()
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return store
}

func TestAdminSystemConfigStillLoadsSchedulerState(t *testing.T) {
	sched := prepareGatewayTestState(t, "https://integrate.api.nvidia.com/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	if err := LoadActiveKeys(context.Background(), sched); err != nil {
		t.Fatalf("load active keys: %v", err)
	}
}
