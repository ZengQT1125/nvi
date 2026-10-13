package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

func encryptedTestAPIKey(t *testing.T, id uint, name, plaintext, status string, probeOnly bool) models.APIKey {
	t.Helper()
	if err := os.Setenv("ENCRYPTION_KEY", testEncryptionKey); err != nil {
		t.Fatalf("set encryption key: %v", err)
	}
	encrypted, err := utils.Encrypt(plaintext, testEncryptionKey)
	if err != nil {
		t.Fatalf("encrypt api key: %v", err)
	}
	return models.APIKey{
		ID:        id,
		Key:       encrypted,
		Name:      name,
		Weight:    1,
		Status:    status,
		ProbeOnly: probeOnly,
	}
}

func TestHealthProbeCandidatesPrefersDedicatedThenShared(t *testing.T) {
	keys := []models.APIKey{
		encryptedTestAPIKey(t, 2, "SHARED", "shared-key", APIKeyStatusActive, false),
		encryptedTestAPIKey(t, 1, "PROBE", "probe-key", APIKeyStatusActive, true),
		// 非 Active 的 Key 不应进入候选
		encryptedTestAPIKey(t, 3, "DEAD-PROBE", "dead-probe-key", APIKeyStatusDead, true),
	}

	candidates, err := healthProbeCandidates(keys)
	if err != nil {
		t.Fatalf("healthProbeCandidates 出错: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("期望 2 个候选（专用 + 共享各一个），实际 %d 个: %+v", len(candidates), candidates)
	}
	if candidates[0].Name != "PROBE" || !candidates[0].Dedicated {
		t.Fatalf("健康检查专用 Key 应排第一，实际 %+v", candidates[0])
	}
	if candidates[1].Name != "SHARED" || candidates[1].Dedicated {
		t.Fatalf("共享 Key 应作为兜底排在专用 Key 之后，实际 %+v", candidates[1])
	}
}

// TestHealthProbeCandidatesFallsBackWhenDedicatedUnusable 覆盖本次修复的核心场景：
// 配置了“健康检查专用”Key 但它不是 Active 时，旧实现直接报错、不回退，
// 导致健康检查页的模型列表永久不可用（而走共享池的业务链路一切正常）。
func TestHealthProbeCandidatesFallsBackWhenDedicatedUnusable(t *testing.T) {
	keys := []models.APIKey{
		encryptedTestAPIKey(t, 1, "PROBE", "probe-key", APIKeyStatusDead, true),
		encryptedTestAPIKey(t, 2, "SHARED", "shared-key", APIKeyStatusActive, false),
	}

	candidates, err := healthProbeCandidates(keys)
	if err != nil {
		t.Fatalf("专用 Key 不可用时应回退到共享池而不是报错，实际: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Name != "SHARED" {
		t.Fatalf("期望回退到 SHARED，实际 %+v", candidates)
	}
	if !candidates[0].Configured {
		t.Fatalf("Configured 应保留 true（用户确实配置过专用 Key），实际 %+v", candidates[0])
	}
	if candidates[0].Dedicated {
		t.Fatalf("回退后的候选不应被标记为专用 Key，实际 %+v", candidates[0])
	}

	selection, err := selectHealthProbeKey(keys)
	if err != nil {
		t.Fatalf("selectHealthProbeKey 出错: %v", err)
	}
	if selection.Name != "SHARED" || selection.Dedicated {
		t.Fatalf("selectHealthProbeKey 应返回回退后的共享 Key，实际 %+v", selection)
	}
}

func TestHealthProbeCandidatesErrorDescribesKeyPool(t *testing.T) {
	keys := []models.APIKey{
		encryptedTestAPIKey(t, 1, "PROBE", "probe-key", APIKeyStatusDead, true),
		encryptedTestAPIKey(t, 2, "SHARED", "shared-key", APIKeyStatusCooling, false),
	}

	_, err := healthProbeCandidates(keys)
	if err == nil {
		t.Fatal("没有任何 Active 上游 Key 时应该报错")
	}
	message := err.Error()
	for _, want := range []string{
		"no active NVIDIA upstream key available",
		"PROBE=Dead·健康专用",
		"SHARED=Cooling",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误信息应包含 %q 以便前端直接展示，实际: %s", want, message)
		}
	}
}

// TestUpstreamModelsFallsBackWhenDedicatedProbeKeyIsDead 端到端验证：
// 专用 Key 为 Dead、共享 Key 为 Active 时，/admin/upstream/models 必须回退并返回 200，
// 而不是像旧实现那样 500 让模型下拉永远为空。
func TestUpstreamModelsFallsBackWhenDedicatedProbeKeyIsDead(t *testing.T) {
	systemHealthStore = &healthReportStore{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer dead-probe-key" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid api key"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []map[string]any{{"id": "meta/llama-3.1-8b-instruct"}},
		})
	}))
	defer upstream.Close()

	prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{
		{Name: "PROBE", Plaintext: "dead-probe-key", Weight: 1, Status: APIKeyStatusDead, ProbeOnly: true},
		{Name: "SHARED", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive},
	})

	app := fiber.New()
	app.Get("/admin/upstream/models", GetUpstreamModels())

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/upstream/models", nil))
	if err != nil {
		t.Fatalf("upstream models request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("应回退到共享 Key 并返回 200，实际 %d（body=%s）", resp.StatusCode, string(body))
	}
	var payload upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upstream models response: %v", err)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("期望 1 个模型，实际 %d", len(payload.Models))
	}
	if payload.ProbeKeyName != "SHARED" {
		t.Fatalf("探测 Key 应记录为回退后的共享 Key，实际 %q", payload.ProbeKeyName)
	}
	if payload.Cached {
		t.Fatalf("这是实时拉取成功的路径，不应标记 cached=true")
	}
}

// TestUpstreamModelsFailsOverBetweenProbeKeys 验证目录拉取会在多个 Key 之间故障转移：
// 专用 Key 可用但被上游限流（429）时，应继续换共享 Key 重试。
func TestUpstreamModelsFailsOverBetweenProbeKeys(t *testing.T) {
	systemHealthStore = &healthReportStore{}

	var mu sync.Mutex
	attempted := make([]string, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		mu.Lock()
		attempted = append(attempted, auth)
		mu.Unlock()
		if auth == "Bearer limited-probe-key" {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limited"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []map[string]any{{"id": "nvidia/nv-embed-v1"}},
		})
	}))
	defer upstream.Close()

	prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{
		{Name: "PROBE", Plaintext: "limited-probe-key", Weight: 1, Status: APIKeyStatusActive, ProbeOnly: true},
		{Name: "SHARED", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive},
	})

	app := fiber.New()
	app.Get("/admin/upstream/models", GetUpstreamModels())

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/upstream/models", nil))
	if err != nil {
		t.Fatalf("upstream models request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("应换 Key 重试并返回 200，实际 %d（body=%s）", resp.StatusCode, string(body))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempted) != 2 {
		t.Fatalf("期望依次尝试专用 Key 与共享 Key 共 2 次，实际 %d 次: %v", len(attempted), attempted)
	}
	if attempted[0] != "Bearer limited-probe-key" || attempted[1] != "Bearer good-key" {
		t.Fatalf("尝试顺序应为「先专用后共享」，实际 %v", attempted)
	}

	var payload upstreamModelsResponse
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode upstream models response: %v", err)
	}
	if payload.ProbeKeyName != "SHARED" {
		t.Fatalf("期望记录最终成功的共享 Key，实际 %q", payload.ProbeKeyName)
	}
}

// TestUpstreamModelsErrorSurfacesKeyPoolStatus 保证所有 Key 都不可用时，
// 返回给前端的错误自带“名称=状态”摘要，不需要用户猜。
func TestUpstreamModelsErrorSurfacesKeyPoolStatus(t *testing.T) {
	systemHealthStore = &healthReportStore{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{
		{Name: "PROBE", Plaintext: "dead-probe-key", Weight: 1, Status: APIKeyStatusDead, ProbeOnly: true},
		{Name: "SHARED", Plaintext: "cooling-key", Weight: 1, Status: APIKeyStatusCooling},
	})

	app := fiber.New()
	app.Get("/admin/upstream/models", GetUpstreamModels())

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/upstream/models", nil))
	if err != nil {
		t.Fatalf("upstream models request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	message := string(body)
	for _, want := range []string{"PROBE=Dead·健康专用", "SHARED=Cooling"} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误响应应包含 %q，实际: %s", want, message)
		}
	}
}
