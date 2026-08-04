package gateway

import (
	"os"
	"testing"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"
)

func TestBuildProxyURLFromModel(t *testing.T) {
	old := os.Getenv("ENCRYPTION_KEY")
	_ = os.Setenv("ENCRYPTION_KEY", "12345678901234567890123456789012")
	defer func() { _ = os.Setenv("ENCRYPTION_KEY", old) }()

	encrypted, err := encryptProxyPassword("secret")
	if err != nil {
		t.Fatalf("encrypt password: %v", err)
	}
	proxyCfg := models.UpstreamProxy{
		ID:       1,
		Name:     "SG Node",
		Type:     "socks5h",
		Host:     "127.0.0.1",
		Port:     1080,
		Username: "alice",
		Password: encrypted,
	}
	url, err := buildProxyURLFromModel(proxyCfg)
	if err != nil {
		t.Fatalf("build proxy URL: %v", err)
	}
	if url != "socks5h://alice:secret@127.0.0.1:1080" {
		t.Fatalf("url = %q", url)
	}
}

func TestRebuildUpstreamRuntimeResolvesPerKeyProxy(t *testing.T) {
	old := os.Getenv("ENCRYPTION_KEY")
	_ = os.Setenv("ENCRYPTION_KEY", "12345678901234567890123456789012")
	defer func() { _ = os.Setenv("ENCRYPTION_KEY", old) }()

	encryptedProxyPassword, err := encryptProxyPassword("secret")
	if err != nil {
		t.Fatalf("encrypt proxy password: %v", err)
	}
	encryptedKey, err := utils.Encrypt("nvapi-test", os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		t.Fatalf("encrypt api key: %v", err)
	}
	store := &db.Store{
		APIKeys: []models.APIKey{{
			ID:        1,
			Name:      "NVIDIA-01",
			Key:       encryptedKey,
			Status:    APIKeyStatusActive,
			ProxyID:   7,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}},
		Proxies: []models.UpstreamProxy{{
			ID:        7,
			Name:      "SG Node",
			ManagedBy: models.CoreManagedByXray,
			Type:      "http",
			Host:      "10.0.0.2",
			Port:      7890,
			Username:  "alice",
			Password:  encryptedProxyPassword,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}},
	}
	if err := rebuildUpstreamRuntime(store); err != nil {
		t.Fatalf("rebuild runtime: %v", err)
	}
	proxyURL, ok := resolveProxyOverrideForPlaintextKey("nvapi-test")
	if !ok {
		t.Fatal("expected override proxy URL")
	}
	if proxyURL != "http://alice:secret@10.0.0.2:7890" {
		t.Fatalf("proxyURL = %q", proxyURL)
	}
	info, ok := lookupKeyRuntimeInfo("nvapi-test")
	if !ok {
		t.Fatal("expected key runtime info")
	}
	if info.ProxyName != "SG Node" {
		t.Fatalf("proxy name = %q", info.ProxyName)
	}
	proxyInfo, ok := effectiveProxyRuntimeInfoForAPIKey(models.DefaultSystemConfig(), "nvapi-test")
	if !ok {
		t.Fatal("expected effective proxy runtime info")
	}
	if proxyInfo.ManagedBy != models.CoreManagedByXray {
		t.Fatalf("managedBy = %q", proxyInfo.ManagedBy)
	}
}

func resetUpstreamRuntimeStateForTest() {
	upstreamRuntimeMu.Lock()
	defer upstreamRuntimeMu.Unlock()
	upstreamRuntimeByKey = map[string]upstreamKeyRuntimeInfo{}
	upstreamRuntimeByProxy = map[uint]upstreamProxyRuntimeInfo{}
	upstreamRuntimeFailoverByID = map[uint]upstreamProxyRuntimeInfo{}
	upstreamRuntimeFailoverByURL = map[string]upstreamProxyRuntimeInfo{}
	upstreamRuntimeCoolingByProxy = map[uint]time.Time{}
}

func TestEffectiveProxyRuntimeInfoUsesFailoverOverride(t *testing.T) {
	resetUpstreamRuntimeStateForTest()
	defer resetUpstreamRuntimeStateForTest()
	old := os.Getenv("ENCRYPTION_KEY")
	_ = os.Setenv("ENCRYPTION_KEY", "12345678901234567890123456789012")
	defer func() { _ = os.Setenv("ENCRYPTION_KEY", old) }()

	encryptedKey, err := utils.Encrypt("nvapi-test", os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		t.Fatalf("encrypt api key: %v", err)
	}
	store := &db.Store{
		APIKeys: []models.APIKey{{ID: 1, Name: "NVIDIA-01", Key: encryptedKey, Status: APIKeyStatusActive, ProxyID: 31, CreatedAt: time.Now(), UpdatedAt: time.Now()}},
		Proxies: []models.UpstreamProxy{
			{ID: 31, Name: "bad", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Host: "127.0.0.1", Port: 21031, Status: models.ProxyStatusEnabled, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: 36, Name: "good", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Host: "127.0.0.1", Port: 21036, Status: models.ProxyStatusEnabled, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		},
	}
	if err := rebuildUpstreamRuntime(store); err != nil {
		t.Fatalf("rebuild runtime: %v", err)
	}
	origin, ok := lookupProxyRuntimeInfo(31)
	if !ok {
		t.Fatal("expected origin proxy runtime info")
	}
	replacement, ok := lookupProxyRuntimeInfo(36)
	if !ok {
		t.Fatal("expected replacement proxy runtime info")
	}
	setProxyRuntimeFailover(origin, replacement)
	info, ok := effectiveProxyRuntimeInfoForAPIKey(models.DefaultSystemConfig(), "nvapi-test")
	if !ok {
		t.Fatal("expected effective proxy runtime info")
	}
	if info.ID != 36 {
		t.Fatalf("proxy id = %d, want 36", info.ID)
	}
	cfg := models.NormalizeSystemConfig(models.SystemConfig{UpstreamProxyURL: "socks5h://127.0.0.1:21031"})
	sysInfo, ok := resolveSystemProxyRuntimeInfo(cfg)
	if !ok {
		t.Fatal("expected system proxy runtime info")
	}
	if sysInfo.ID != 36 {
		t.Fatalf("system proxy id = %d, want 36", sysInfo.ID)
	}
}

func TestSelectAlternativeManagedProxyFromStorePrefersHealthyFastest(t *testing.T) {
	resetUpstreamRuntimeStateForTest()
	defer resetUpstreamRuntimeStateForTest()
	store := &db.Store{Proxies: []models.UpstreamProxy{
		{ID: 31, Name: "bad", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Host: "127.0.0.1", Port: 21031, Status: models.ProxyStatusEnabled, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: 33, Name: "slow", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Host: "127.0.0.1", Port: 21033, Status: models.ProxyStatusEnabled, LastTest: &models.ProxyTestRecord{Success: true, ResponseTime: 520}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: 36, Name: "fast", ManagedBy: models.CoreManagedByXray, Type: "socks5h", Host: "127.0.0.1", Port: 21036, Status: models.ProxyStatusEnabled, LastTest: &models.ProxyTestRecord{Success: true, ResponseTime: 180}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}}
	origin := upstreamProxyRuntimeInfo{ID: 31, URL: "socks5h://127.0.0.1:21031", ManagedBy: models.CoreManagedByXray}
	candidate, ok := selectAlternativeManagedProxyFromStore(store, origin)
	if !ok {
		t.Fatal("expected alternative proxy")
	}
	if candidate.ID != 36 {
		t.Fatalf("candidate id = %d, want 36", candidate.ID)
	}
	markProxyRuntimeCooling(36, time.Minute)
	candidate, ok = selectAlternativeManagedProxyFromStore(store, origin)
	if !ok {
		t.Fatal("expected fallback proxy after cooling")
	}
	if candidate.ID != 33 {
		t.Fatalf("candidate id = %d, want 33", candidate.ID)
	}
}
