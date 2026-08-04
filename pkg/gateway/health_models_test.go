package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"
	"nvidia-api-gateway/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

const testEncryptionKey = "12345678901234567890123456789012"

func TestGetHealthReportDoesNotProbeOnColdLoad(t *testing.T) {
	systemHealthStore = &healthReportStore{}
	requestCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})

	app := fiber.New()
	app.Get("/admin/health/report", GetHealthReport(sched))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/health/report", nil))
	if err != nil {
		t.Fatalf("cold health report request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var payload healthReport
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode cold health report: %v", err)
	}
	if requestCount != 0 {
		t.Fatalf("expected cold GET to avoid upstream probing, got %d requests", requestCount)
	}
	if len(payload.Checks) != 0 {
		t.Fatalf("expected no live checks on cold GET, got %d", len(payload.Checks))
	}
}

func TestHealthProbeUsesDedicatedKeyAndSkipsScheduler(t *testing.T) {
	systemHealthStore = &healthReportStore{}
	authHeaders := make([]string, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/models":
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]any{{"id": "nvidia/nemotron-mini-4b-instruct"}, {"id": "nvidia/nv-embed-v1"}},
			})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"O\"}}]}\n\n"))
		case "/v1/embeddings":
			writeJSON(w, http.StatusOK, map[string]any{
				"model": "nvidia/nv-embed-v1",
				"data":  []map[string]any{{"embedding": []float64{0.1, 0.2}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{
		{Name: "NVIDIA-shared", Plaintext: "shared-key", Weight: 1, Status: APIKeyStatusActive},
		{Name: "NVIDIA-probe", Plaintext: "probe-key", Weight: 1, Status: APIKeyStatusActive, ProbeOnly: true},
	})
	stats, err := sched.Stats(context.Background())
	if err != nil {
		t.Fatalf("scheduler stats failed: %v", err)
	}
	if stats.Active != 1 {
		t.Fatalf("expected scheduler to load only shared keys, got %d active", stats.Active)
	}

	report, err := buildSystemHealthReport(context.Background(), sched, healthRunRequest{Scope: "all", Protocol: "auto"})
	if err != nil {
		t.Fatalf("build system health report failed: %v", err)
	}
	if !report.ProbeKeyDedicated {
		t.Fatalf("expected dedicated probe key to be selected")
	}
	if report.ProbeKeyName != "NVIDIA-probe" {
		t.Fatalf("expected probe key NVIDIA-probe, got %s", report.ProbeKeyName)
	}
	for _, header := range authHeaders {
		if header != "Bearer probe-key" {
			t.Fatalf("expected all health probe requests to use dedicated key, got %q", header)
		}
	}
}

func TestUpstreamRuntimeEventLabels(t *testing.T) {
	systemUpstreamRuntimeStore = &upstreamRuntimeStore{}
	recordUpstreamRuntimeEvent("chat.nonstream", "first_byte_timeout", "", false, 0, "timeout")
	snapshot := systemUpstreamRuntimeStore.snapshot(nil)
	if snapshot.LastEvent == nil {
		t.Fatal("expected last event")
	}
	if snapshot.LastEvent.OperationLabel != upstreamOperationLabel("chat.nonstream") {
		t.Fatalf("unexpected operation label: %q", snapshot.LastEvent.OperationLabel)
	}
	if snapshot.LastEvent.StageLabel != upstreamStageLabel("first_byte_timeout") {
		t.Fatalf("unexpected stage label: %q", snapshot.LastEvent.StageLabel)
	}
}

func TestAdminUpstreamModelsAndHealthRuns(t *testing.T) {
	systemHealthStore = &healthReportStore{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]any{
					{"id": "meta/llama-3.1-8b-instruct"},
					{"id": "nvidia/nv-embed-v1"},
				},
			})
		case "/v1/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{
				"id":    "chatcmpl_test",
				"model": "meta/llama-3.1-8b-instruct",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "OK",
					},
					"finish_reason": "stop",
				}},
			})
		case "/v1/embeddings":
			writeJSON(w, http.StatusOK, map[string]any{
				"model": "nvidia/nv-embed-v1",
				"data": []map[string]any{{
					"embedding": []float64{0.1, 0.2, 0.3},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})

	app := fiber.New()
	app.Get("/admin/upstream/models", GetUpstreamModels())
	app.Post("/admin/health/report/run", RunHealthReport(sched))
	app.Get("/admin/health/report", GetHealthReport(sched))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/upstream/models", nil))
	if err != nil {
		t.Fatalf("upstream models request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var modelsPayload upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsPayload); err != nil {
		t.Fatalf("decode upstream models response: %v", err)
	}
	if len(modelsPayload.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(modelsPayload.Models))
	}
	if !modelsPayload.Models[0].SupportsChatCandidate {
		t.Fatalf("expected chat candidate flag on %s", modelsPayload.Models[0].ID)
	}
	foundEmbedding := false
	for _, item := range modelsPayload.Models {
		if item.ID == "nvidia/nv-embed-v1" {
			foundEmbedding = item.SupportsEmbeddingsCandidate
		}
	}
	if !foundEmbedding {
		t.Fatalf("expected embedding candidate flag for nvidia/nv-embed-v1")
	}

	fullReq := httptest.NewRequest(http.MethodPost, "/admin/health/report/run", strings.NewReader(`{"scope":"all","protocol":"auto"}`))
	fullReq.Header.Set("Content-Type", "application/json")
	fullResp, err := app.Test(fullReq)
	if err != nil {
		t.Fatalf("full health run failed: %v", err)
	}
	defer fullResp.Body.Close()
	var fullReport healthReport
	if err := json.NewDecoder(fullResp.Body).Decode(&fullReport); err != nil {
		t.Fatalf("decode full report: %v", err)
	}
	if fullReport.FullSweep == nil {
		t.Fatalf("expected fullSweep in full health run")
	}
	if fullReport.FullSweep.Summary.Total != 2 {
		t.Fatalf("expected full sweep total 2, got %d", fullReport.FullSweep.Summary.Total)
	}
	if fullReport.ActiveRun == nil || fullReport.ActiveRun.Summary.Total != 2 {
		t.Fatalf("expected activeRun total 2, got %+v", fullReport.ActiveRun)
	}

	singleReq := httptest.NewRequest(http.MethodPost, "/admin/health/report/run", strings.NewReader(`{"scope":"single","modelId":"meta/llama-3.1-8b-instruct","protocol":"chat"}`))
	singleReq.Header.Set("Content-Type", "application/json")
	singleResp, err := app.Test(singleReq)
	if err != nil {
		t.Fatalf("single health run failed: %v", err)
	}
	defer singleResp.Body.Close()
	var singleReport healthReport
	if err := json.NewDecoder(singleResp.Body).Decode(&singleReport); err != nil {
		t.Fatalf("decode single report: %v", err)
	}
	if singleReport.ActiveRun == nil || singleReport.ActiveRun.Summary.Total != 1 {
		t.Fatalf("expected single activeRun total 1, got %+v", singleReport.ActiveRun)
	}
	if singleReport.FullSweep == nil || singleReport.FullSweep.Summary.Total != 2 {
		t.Fatalf("expected preserved fullSweep total 2, got %+v", singleReport.FullSweep)
	}

	systemHealthStore = &healthReportStore{}
	persistedResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/health/report", nil))
	if err != nil {
		t.Fatalf("persisted health report request failed: %v", err)
	}
	defer persistedResp.Body.Close()
	var persistedReport healthReport
	if err := json.NewDecoder(persistedResp.Body).Decode(&persistedReport); err != nil {
		t.Fatalf("decode persisted report: %v", err)
	}
	if persistedReport.FullSweep == nil || persistedReport.FullSweep.Summary.Total != 2 {
		t.Fatalf("expected persisted fullSweep total 2 after refresh, got %+v", persistedReport.FullSweep)
	}
}

// TestRunHealthReportSkipsModelSweepWhenUpstreamModelsFail 保证：
// 上游 /models 不可达时，不会再去对所有缓存里的模型做 N 次单独探测——
// 之前那条路径会让 119 个模型的 45s 串行超时，前端表现为 "卡住、永远拉不出结果"。
// 同时 GetUpstreamModels 应该返回上一次成功拉到的目录加 warning，而不是 500。
func TestRunHealthReportSkipsModelSweepWhenUpstreamModelsFail(t *testing.T) {
	systemHealthStore = &healthReportStore{}

	failModels := false
	perModelCallCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if failModels {
				http.Error(w, "models temporarily unavailable", http.StatusBadGateway)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]any{
					{"id": "meta/llama-3.1-8b-instruct"},
					{"id": "nvidia/nv-embed-v1"},
				},
			})
		case "/v1/chat/completions":
			perModelCallCount++
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"O\"}}]}\n\n"))
		case "/v1/embeddings":
			perModelCallCount++
			writeJSON(w, http.StatusOK, map[string]any{
				"model": "nvidia/nv-embed-v1",
				"data":  []map[string]any{{"embedding": []float64{0.1, 0.2}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	sched := prepareGatewayTestState(t, upstream.URL+"/v1", []testAPIKey{{Name: "NVIDIA-01", Plaintext: "good-key", Weight: 1, Status: APIKeyStatusActive}})
	app := fiber.New()
	app.Get("/admin/upstream/models", GetUpstreamModels())
	app.Post("/admin/health/report/run", RunHealthReport(sched))

	// 1) 先跑一次成功的全量扫描，把目录写到内存缓存里。
	first := httptest.NewRequest(http.MethodPost, "/admin/health/report/run", strings.NewReader(`{"scope":"all","protocol":"auto"}`))
	first.Header.Set("Content-Type", "application/json")
	firstResp, err := app.Test(first)
	if err != nil {
		t.Fatalf("first health run failed: %v", err)
	}
	defer firstResp.Body.Close()
	var firstReport healthReport
	if err := json.NewDecoder(firstResp.Body).Decode(&firstReport); err != nil {
		t.Fatalf("decode first report: %v", err)
	}
	if firstReport.FullSweep == nil || firstReport.FullSweep.Summary.Total != 2 {
		t.Fatalf("expected initial fullSweep total 2, got %+v", firstReport.FullSweep)
	}
	if len(firstReport.ModelCatalog) != 2 {
		t.Fatalf("expected catalog of 2, got %d", len(firstReport.ModelCatalog))
	}

	// 2) 把上游 /models 切到 502，再跑一次全量扫描。
	// 关键期望：本次不再触发任何 /chat/completions or /embeddings 探测。
	failModels = true
	perModelCallCount = 0
	second := httptest.NewRequest(http.MethodPost, "/admin/health/report/run", strings.NewReader(`{"scope":"all","protocol":"auto"}`))
	second.Header.Set("Content-Type", "application/json")
	secondResp, err := app.Test(second)
	if err != nil {
		t.Fatalf("second health run failed: %v", err)
	}
	defer secondResp.Body.Close()
	var secondReport healthReport
	if err := json.NewDecoder(secondResp.Body).Decode(&secondReport); err != nil {
		t.Fatalf("decode second report: %v", err)
	}
	// 基线 3 个 checks 里 nvidia_models 一定要失败。
	var modelsCheck *healthCheckResult
	for i := range secondReport.Checks {
		if secondReport.Checks[i].ID == "nvidia_models" {
			modelsCheck = &secondReport.Checks[i]
			break
		}
	}
	if modelsCheck == nil || modelsCheck.Success {
		t.Fatalf("expected nvidia_models check to be failed, got %+v", modelsCheck)
	}
	// 关键：扫描被跳过，per-model 端点完全没有被打到。
	// 不要看 chat/embeddings baseline 探测——它们靠 chooseChatHealthModel 在 availableModels 为空时会 Skipped。
	if perModelCallCount != 0 {
		t.Fatalf("expected zero per-model probes after /models failure, got %d", perModelCallCount)
	}

	// 3) GetUpstreamModels 在上游挂掉的情况下要降级返回缓存目录 + warning，
	// 而不是返回 500 让前端整个下拉空掉。
	cachedResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/upstream/models", nil))
	if err != nil {
		t.Fatalf("cached upstream models request failed: %v", err)
	}
	defer cachedResp.Body.Close()
	if cachedResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with cached fallback, got %d", cachedResp.StatusCode)
	}
	var cachedPayload upstreamModelsResponse
	if err := json.NewDecoder(cachedResp.Body).Decode(&cachedPayload); err != nil {
		t.Fatalf("decode cached models: %v", err)
	}
	if !cachedPayload.Cached {
		t.Fatalf("expected cached=true on fallback, got %+v", cachedPayload)
	}
	if cachedPayload.Warning == "" {
		t.Fatalf("expected warning to be set on fallback")
	}
	if len(cachedPayload.Models) != 2 {
		t.Fatalf("expected 2 cached models, got %d", len(cachedPayload.Models))
	}
}

type testAPIKey struct {
	Name      string
	Plaintext string
	Weight    float64
	Status    string
	ProbeOnly bool
}

func prepareGatewayTestState(t *testing.T, upstreamBaseURL string, apiKeys []testAPIKey) *scheduler.Scheduler {
	t.Helper()
	if err := os.Setenv("ENCRYPTION_KEY", testEncryptionKey); err != nil {
		t.Fatalf("set encryption key: %v", err)
	}
	storePath := filepath.Join(t.TempDir(), "gateway.json")
	db.InitDB(storePath)
	if err := db.UpdateStore(func(store *db.Store) error {
		store.SystemConfig = models.NormalizeSystemConfig(models.SystemConfig{
			UpstreamBaseURL:       upstreamBaseURL,
			SchedulerStrategy:     models.DefaultSchedulerStrategy,
			MaxRetries:            3,
			MaxConcurrency:        2,
			RequestTimeoutSecond:  10,
			EnableOpenAI:          true,
			EnableClaude:          true,
			EnableGemini:          true,
			AnonymousAccess:       false,
			FirstByteTimeoutMs:    50,
			HealthProbeTimeoutSec: 2,
		})
		store.APIKeys = make([]models.APIKey, 0, len(apiKeys))
		store.NextAPIID = 1
		for _, item := range apiKeys {
			encrypted, err := utils.Encrypt(item.Plaintext, testEncryptionKey)
			if err != nil {
				return err
			}
			store.APIKeys = append(store.APIKeys, models.APIKey{
				ID:        store.NextAPIID,
				Key:       encrypted,
				Name:      item.Name,
				Weight:    item.Weight,
				Status:    item.Status,
				ProbeOnly: item.ProbeOnly,
			})
			store.NextAPIID++
		}
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}
	sched := scheduler.NewScheduler(nil)
	if err := LoadActiveKeys(context.Background(), sched); err != nil {
		t.Fatalf("load active keys: %v", err)
	}
	return sched
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
