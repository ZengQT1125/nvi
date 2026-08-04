package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvidia-api-gateway/pkg/models"
)

func TestXrayArchiveCandidatesPreferOfficialAndSeedArchive(t *testing.T) {
	dir := t.TempDir()
	old := os.Getenv("XRAY_CORE_DIR")
	if err := os.Setenv("XRAY_CORE_DIR", dir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	defer func() { _ = os.Setenv("XRAY_CORE_DIR", old) }()

	files := []string{"misc.zip", "api_asset_download.zip", "Xray-windows-64.zip"}
	for idx, name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
		modTime := time.Unix(int64(idx+1), 0)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("Chtimes(%s): %v", name, err)
		}
	}

	candidates, err := xrayArchiveCandidates("Xray-windows-64.zip")
	if err != nil {
		t.Fatalf("xrayArchiveCandidates() error = %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(candidates))
	}
	if filepath.Base(candidates[0]) != "Xray-windows-64.zip" {
		t.Fatalf("expected official archive first, got %s", filepath.Base(candidates[0]))
	}
	if filepath.Base(candidates[1]) != "api_asset_download.zip" {
		t.Fatalf("expected seed archive second, got %s", filepath.Base(candidates[1]))
	}
}

func TestDisableIncompatibleCoreProfilesAutoDisablesLegacyShadowsocksCipher(t *testing.T) {
	input := []models.CoreProfile{{
		ID:        39,
		Name:      "legacy-ss",
		Protocol:  "shadowsocks",
		Status:    models.CoreProfileStatusEnabled,
		Server:    "127.0.0.1",
		Port:      443,
		Method:    "aes-256-cfb",
		UpdatedAt: time.Unix(1, 0),
	}}

	output := disableIncompatibleCoreProfiles(input)
	if len(output) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(output))
	}
	if output[0].Status != models.CoreProfileStatusDisabled {
		t.Fatalf("expected incompatible profile to be disabled, got %s", output[0].Status)
	}
	if !strings.Contains(output[0].Remarks, "aes-256-cfb") {
		t.Fatalf("expected remarks to mention cipher, got %q", output[0].Remarks)
	}
	if !output[0].UpdatedAt.After(input[0].UpdatedAt) {
		t.Fatalf("expected updated time to advance, before=%s after=%s", input[0].UpdatedAt, output[0].UpdatedAt)
	}
}

func TestValidateCoreProfileRejectsUnsupportedShadowsocksMethod(t *testing.T) {
	profile := models.NormalizeCoreProfile(models.CoreProfile{
		Name:     "legacy-ss",
		Protocol: "shadowsocks",
		Status:   models.CoreProfileStatusEnabled,
		Server:   "127.0.0.1",
		Port:     443,
		Method:   "aes-256-cfb",
	})

	err := validateCoreProfile(profile, "", "secret")
	if err == nil {
		t.Fatal("expected unsupported cipher to be rejected")
	}
	if !strings.Contains(err.Error(), "aes-256-cfb") {
		t.Fatalf("expected error to mention cipher, got %v", err)
	}
}
