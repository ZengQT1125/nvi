package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"

	"github.com/gofiber/fiber/v2"
)

const (
	proxyImportStatusIdle      = "idle"
	proxyImportStatusRunning   = "running"
	proxyImportStatusSucceeded = "succeeded"
	proxyImportStatusFailed    = "failed"

	proxyImportPhaseIdle       = "idle"
	proxyImportPhaseFetching   = "fetching"
	proxyImportPhaseTesting    = "testing"
	proxyImportPhasePersisting = "persisting"
	proxyImportPhaseCompleted  = "completed"
	proxyImportPhaseFailed     = "failed"

	proxyImportTriggerManual    = "manual"
	proxyImportTriggerScheduled = "scheduled"
)

var errProxyImportAlreadyRunning = errors.New("自动抓取任务正在执行中，请等待当前任务完成")

type proxyImportScheduleRequest struct {
	Enabled                        bool     `json:"enabled"`
	Times                          []string `json:"times"`
	Mode                           string   `json:"mode,omitempty"`
	Group                          string   `json:"group,omitempty"`
	Limit                          int      `json:"limit,omitempty"`
	Concurrency                    int      `json:"concurrency,omitempty"`
	TimeoutSeconds                 int      `json:"timeoutSeconds,omitempty"`
	RetryCount                     int      `json:"retryCount,omitempty"`
	CleanupEnabled                 bool     `json:"cleanupEnabled,omitempty"`
	CleanupMaxLatencyMs            int      `json:"cleanupMaxLatencyMs,omitempty"`
	CleanupDeleteFailedAutoProxies bool     `json:"cleanupDeleteFailedAutoProxies,omitempty"`
}

type proxyImportTaskRequest struct {
	Mode                           string `json:"mode"`
	Group                          string `json:"group"`
	Limit                          int    `json:"limit"`
	Concurrency                    int    `json:"concurrency"`
	TimeoutSeconds                 int    `json:"timeoutSeconds"`
	RetryCount                     int    `json:"retryCount"`
	CleanupEnabled                 bool   `json:"cleanupEnabled,omitempty"`
	CleanupMaxLatencyMs            int    `json:"cleanupMaxLatencyMs,omitempty"`
	CleanupDeleteFailedAutoProxies bool   `json:"cleanupDeleteFailedAutoProxies,omitempty"`
}

type proxyImportTaskState struct {
	ID                  string                  `json:"id,omitempty"`
	Status              string                  `json:"status"`
	Phase               string                  `json:"phase"`
	Trigger             string                  `json:"trigger,omitempty"`
	Progress            int                     `json:"progress"`
	Message             string                  `json:"message,omitempty"`
	Error               string                  `json:"error,omitempty"`
	TotalSources        int                     `json:"totalSources,omitempty"`
	CompletedSources    int                     `json:"completedSources,omitempty"`
	CandidateCount      int                     `json:"candidateCount,omitempty"`
	TestedCount         int                     `json:"testedCount,omitempty"`
	AvailableCount      int                     `json:"availableCount,omitempty"`
	FailedCount         int                     `json:"failedCount,omitempty"`
	PersistedCount      int                     `json:"persistedCount,omitempty"`
	ImportedCount       int                     `json:"importedCount,omitempty"`
	UpdatedCount        int                     `json:"updatedCount,omitempty"`
	MatchedManual       int                     `json:"matchedManualCount,omitempty"`
	CleanedSlowCount    int                     `json:"cleanedSlowCount,omitempty"`
	CleanedFailedCount  int                     `json:"cleanedFailedCount,omitempty"`
	CleanupDeletedCount int                     `json:"cleanupDeletedCount,omitempty"`
	UnboundKeyCount     int                     `json:"unboundKeyCount,omitempty"`
	StartedAt           time.Time               `json:"startedAt,omitempty"`
	FinishedAt          time.Time               `json:"finishedAt,omitempty"`
	Request             proxyImportTaskRequest  `json:"request"`
	Summary             *freeProxyImportSummary `json:"summary,omitempty"`
}

type proxyImportStateResponse struct {
	Task      proxyImportTaskState             `json:"task"`
	Schedule  models.ProxyImportSchedule       `json:"schedule"`
	Logs      []models.ProxyImportExecutionLog `json:"logs"`
	NextRunAt *time.Time                       `json:"nextRunAt,omitempty"`
}

type ProxyImportManager struct {
	sched *scheduler.Scheduler

	mu   sync.RWMutex
	task proxyImportTaskState
}

func NewProxyImportManager(sched *scheduler.Scheduler) *ProxyImportManager {
	return &ProxyImportManager{
		sched: sched,
		task: proxyImportTaskState{
			Status:   proxyImportStatusIdle,
			Phase:    proxyImportPhaseIdle,
			Progress: 0,
			Message:  "暂无后台任务",
			Request: proxyImportTaskRequest{
				Mode:                           models.DefaultProxyImportMode,
				Group:                          models.DefaultProxyImportGroup,
				Limit:                          models.DefaultProxyImportLimit,
				Concurrency:                    models.DefaultProxyImportConcurrency,
				TimeoutSeconds:                 models.DefaultProxyImportTimeoutSeconds,
				RetryCount:                     models.DefaultProxyImportRetryCount,
				CleanupEnabled:                 false,
				CleanupMaxLatencyMs:            models.DefaultProxyImportCleanupLatency,
				CleanupDeleteFailedAutoProxies: false,
			},
		},
	}
}

func (m *ProxyImportManager) Start(ctx context.Context) {
	m.runScheduledIfNeeded()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runScheduledIfNeeded()
		}
	}
}

func (m *ProxyImportManager) Snapshot() proxyImportStateResponse {
	schedule, logs, err := loadProxyImportMeta()
	if err != nil {
		schedule = models.DefaultProxyImportSchedule()
		logs = make([]models.ProxyImportExecutionLog, 0)
	}
	return proxyImportStateResponse{
		Task:      m.currentTask(),
		Schedule:  schedule,
		Logs:      logs,
		NextRunAt: computeNextProxyImportRun(schedule, time.Now()),
	}
}

func (m *ProxyImportManager) StartManualImport(req importFreeProxiesRequest) (proxyImportTaskState, error) {
	opts, err := normalizeFreeProxyImportOptions(req)
	if err != nil {
		return proxyImportTaskState{}, err
	}
	return m.startImport(opts, proxyImportTriggerManual, time.Now())
}

func (m *ProxyImportManager) UpdateSchedule(req proxyImportScheduleRequest) (models.ProxyImportSchedule, error) {
	schedule, err := normalizeProxyImportScheduleRequest(req)
	if err != nil {
		return models.ProxyImportSchedule{}, err
	}
	if err := db.UpdateStore(func(store *db.Store) error {
		existing := models.NormalizeProxyImportSchedule(store.ProxyImportSchedule)
		schedule.LastRunAt = existing.LastRunAt
		store.ProxyImportSchedule = schedule
		return nil
	}); err != nil {
		return models.ProxyImportSchedule{}, err
	}
	return schedule, nil
}

func GetProxyImportState(manager *ProxyImportManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(manager.Snapshot())
	}
}

func UpdateProxyImportSchedule(manager *ProxyImportManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req proxyImportScheduleRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		schedule, err := manager.UpdateSchedule(req)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{
			"message":   "定时抓取配置已保存。",
			"schedule":  schedule,
			"nextRunAt": computeNextProxyImportRun(schedule, time.Now()),
		})
	}
}

func ClearProxyImportLogs() fiber.Handler {
	return func(c *fiber.Ctx) error {
		clearedCount := 0
		if err := db.UpdateStore(func(store *db.Store) error {
			clearedCount = len(store.ProxyImportLogs)
			store.ProxyImportLogs = make([]models.ProxyImportExecutionLog, 0)
			return nil
		}); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "清空最近执行日志失败"})
		}
		return c.JSON(fiber.Map{
			"message":      "最近执行日志已清空。",
			"clearedCount": clearedCount,
		})
	}
}

func (m *ProxyImportManager) runScheduledIfNeeded() {
	schedule, _, err := loadProxyImportMeta()
	if err != nil || !schedule.Enabled || len(schedule.Times) == 0 {
		return
	}
	now := time.Now()
	if !shouldTriggerProxyImportSchedule(schedule, now) {
		return
	}
	opts, err := normalizeFreeProxyImportOptions(importFreeProxiesRequest{
		Mode:                           schedule.Mode,
		Group:                          schedule.Group,
		Limit:                          schedule.Limit,
		Concurrency:                    schedule.Concurrency,
		TimeoutSeconds:                 schedule.TimeoutSeconds,
		RetryCount:                     schedule.RetryCount,
		CleanupEnabled:                 schedule.CleanupEnabled,
		CleanupMaxLatencyMs:            schedule.CleanupMaxLatencyMs,
		CleanupDeleteFailedAutoProxies: schedule.CleanupDeleteFailedAutoProxies,
	})
	if err != nil {
		return
	}
	if _, err := m.startImport(opts, proxyImportTriggerScheduled, now); err != nil {
		return
	}
	_ = db.UpdateStore(func(store *db.Store) error {
		cfg := models.NormalizeProxyImportSchedule(store.ProxyImportSchedule)
		cfg.LastRunAt = now
		store.ProxyImportSchedule = cfg
		return nil
	})
}

func (m *ProxyImportManager) startImport(opts freeProxyImportOptions, trigger string, now time.Time) (proxyImportTaskState, error) {
	request := proxyImportTaskRequest{
		Mode:                           string(opts.Mode),
		Group:                          opts.Group,
		Limit:                          opts.Limit,
		Concurrency:                    opts.Concurrency,
		TimeoutSeconds:                 int(opts.Timeout / time.Second),
		RetryCount:                     opts.RetryCount,
		CleanupEnabled:                 opts.CleanupEnabled,
		CleanupMaxLatencyMs:            opts.CleanupMaxLatencyMs,
		CleanupDeleteFailedAutoProxies: opts.CleanupDeleteFailedAutoProxies,
	}

	m.mu.Lock()
	if m.task.Status == proxyImportStatusRunning {
		task := cloneProxyImportTask(m.task)
		m.mu.Unlock()
		return task, errProxyImportAlreadyRunning
	}
	task := proxyImportTaskState{
		ID:        fmt.Sprintf("proxy-import-%d", now.UnixNano()),
		Status:    proxyImportStatusRunning,
		Phase:     proxyImportPhaseFetching,
		Trigger:   trigger,
		Progress:  0,
		Message:   "正在准备抓取代理源...",
		StartedAt: now,
		Request:   request,
	}
	m.task = task
	m.mu.Unlock()

	go m.runImportTask(task.ID, opts, trigger)
	return task, nil
}

func (m *ProxyImportManager) runImportTask(taskID string, opts freeProxyImportOptions, trigger string) {
	hooks := freeProxyImportHooks{
		OnFetchStart: func(total int) {
			m.updateTask(taskID, func(task *proxyImportTaskState) {
				task.Phase = proxyImportPhaseFetching
				task.TotalSources = total
				task.CompletedSources = 0
				task.Message = fmt.Sprintf("正在抓取 %d 个代理源...", total)
				task.Progress = clampProxyImportProgress(0)
			})
		},
		OnFetchProgress: func(completed, total int) {
			m.updateTask(taskID, func(task *proxyImportTaskState) {
				task.Phase = proxyImportPhaseFetching
				task.TotalSources = total
				task.CompletedSources = completed
				task.Message = fmt.Sprintf("代理源抓取中：%d / %d", completed, total)
				task.Progress = clampProxyImportProgress(scaleProgress(completed, total, 0, 20))
			})
		},
		OnCandidatesReady: func(candidateCount, sourceErrors int) {
			m.updateTask(taskID, func(task *proxyImportTaskState) {
				task.CandidateCount = candidateCount
				task.Message = fmt.Sprintf("候选代理 %d 个，源错误 %d 个，开始测速...", candidateCount, sourceErrors)
				if candidateCount == 0 {
					task.Progress = 100
				} else {
					task.Phase = proxyImportPhaseTesting
					task.Progress = maxInt(task.Progress, 20)
				}
			})
		},
		OnTestProgress: func(tested, total, available, failed int) {
			m.updateTask(taskID, func(task *proxyImportTaskState) {
				task.Phase = proxyImportPhaseTesting
				task.CandidateCount = total
				task.TestedCount = tested
				task.AvailableCount = available
				task.FailedCount = failed
				task.Message = fmt.Sprintf("代理测速中：%d / %d，可用 %d，失败 %d", tested, total, available, failed)
				task.Progress = clampProxyImportProgress(scaleProgress(tested, total, 20, 90))
			})
		},
		OnPersistProgress: func(done, total, imported, updated, matchedManual int) {
			m.updateTask(taskID, func(task *proxyImportTaskState) {
				task.Phase = proxyImportPhasePersisting
				task.PersistedCount = done
				task.ImportedCount = imported
				task.UpdatedCount = updated
				task.MatchedManual = matchedManual
				task.Message = fmt.Sprintf("正在写入代理池：%d / %d，新增 %d，更新 %d", done, total, imported, updated)
				task.Progress = clampProxyImportProgress(scaleProgress(done, total, 90, 99))
			})
		},
	}

	summary, err := importFreeUpstreamProxiesWithHooks(context.Background(), opts, hooks)
	if err != nil {
		m.failTask(taskID, "自动抓取免费代理失败: "+err.Error(), nil)
		return
	}
	if summary.ImportedCount > 0 || summary.UpdatedCount > 0 || summary.CleanupDeletedCount > 0 {
		if err := LoadActiveKeys(context.Background(), m.sched); err != nil {
			m.failTask(taskID, "代理已导入，但刷新运行时失败: "+err.Error(), &summary)
			return
		}
	}
	m.completeTask(taskID, summary, buildProxyImportMessage(summary, trigger))
}

func (m *ProxyImportManager) completeTask(taskID string, summary freeProxyImportSummary, message string) {
	now := time.Now()
	var finalTask proxyImportTaskState
	m.updateTask(taskID, func(task *proxyImportTaskState) {
		task.Status = proxyImportStatusSucceeded
		task.Phase = proxyImportPhaseCompleted
		task.Progress = 100
		task.Message = message
		task.Error = ""
		task.FinishedAt = now
		task.CandidateCount = summary.CandidateCount
		task.TestedCount = summary.TestedCount
		task.AvailableCount = summary.AvailableCount
		task.FailedCount = summary.FailedCount
		task.ImportedCount = summary.ImportedCount
		task.UpdatedCount = summary.UpdatedCount
		task.MatchedManual = summary.MatchedManualCount
		task.CleanedSlowCount = summary.CleanedSlowCount
		task.CleanedFailedCount = summary.CleanedFailedCount
		task.CleanupDeletedCount = summary.CleanupDeletedCount
		task.UnboundKeyCount = summary.UnboundKeyCount
		copySummary := summary
		task.Summary = &copySummary
		finalTask = cloneProxyImportTask(*task)
	})
	m.appendExecutionLog(finalTask)
}

func (m *ProxyImportManager) failTask(taskID string, message string, summary *freeProxyImportSummary) {
	now := time.Now()
	var finalTask proxyImportTaskState
	m.updateTask(taskID, func(task *proxyImportTaskState) {
		task.Status = proxyImportStatusFailed
		task.Phase = proxyImportPhaseFailed
		task.Progress = 100
		task.Message = message
		task.Error = message
		task.FinishedAt = now
		if summary != nil {
			copySummary := *summary
			task.CandidateCount = copySummary.CandidateCount
			task.TestedCount = copySummary.TestedCount
			task.AvailableCount = copySummary.AvailableCount
			task.FailedCount = copySummary.FailedCount
			task.ImportedCount = copySummary.ImportedCount
			task.UpdatedCount = copySummary.UpdatedCount
			task.MatchedManual = copySummary.MatchedManualCount
			task.CleanedSlowCount = copySummary.CleanedSlowCount
			task.CleanedFailedCount = copySummary.CleanedFailedCount
			task.CleanupDeletedCount = copySummary.CleanupDeletedCount
			task.UnboundKeyCount = copySummary.UnboundKeyCount
			task.Summary = &copySummary
		}
		finalTask = cloneProxyImportTask(*task)
	})
	m.appendExecutionLog(finalTask)
}

func (m *ProxyImportManager) currentTask() proxyImportTaskState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneProxyImportTask(m.task)
}

func (m *ProxyImportManager) updateTask(taskID string, updater func(*proxyImportTaskState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.task.ID != taskID {
		return
	}
	updater(&m.task)
}

func cloneProxyImportTask(task proxyImportTaskState) proxyImportTaskState {
	copyTask := task
	if task.Summary != nil {
		copySummary := *task.Summary
		copyTask.Summary = &copySummary
	}
	return copyTask
}

func loadProxyImportMeta() (models.ProxyImportSchedule, []models.ProxyImportExecutionLog, error) {
	store, err := db.ReadStore()
	if err != nil {
		return models.ProxyImportSchedule{}, nil, err
	}
	logs := make([]models.ProxyImportExecutionLog, len(store.ProxyImportLogs))
	copy(logs, store.ProxyImportLogs)
	return models.NormalizeProxyImportSchedule(store.ProxyImportSchedule), logs, nil
}

func normalizeProxyImportScheduleRequest(req proxyImportScheduleRequest) (models.ProxyImportSchedule, error) {
	times, err := normalizeProxyImportScheduleTimes(req.Times)
	if err != nil {
		return models.ProxyImportSchedule{}, err
	}
	schedule := models.NormalizeProxyImportSchedule(models.ProxyImportSchedule{
		Enabled:                        req.Enabled,
		Times:                          times,
		Mode:                           req.Mode,
		Group:                          req.Group,
		Limit:                          req.Limit,
		Concurrency:                    req.Concurrency,
		TimeoutSeconds:                 req.TimeoutSeconds,
		RetryCount:                     req.RetryCount,
		CleanupEnabled:                 req.CleanupEnabled,
		CleanupMaxLatencyMs:            req.CleanupMaxLatencyMs,
		CleanupDeleteFailedAutoProxies: req.CleanupDeleteFailedAutoProxies,
		UpdatedAt:                      time.Now(),
	})
	if schedule.Enabled && len(schedule.Times) == 0 {
		return models.ProxyImportSchedule{}, fmt.Errorf("开启定时抓取时，至少填写一个时间")
	}
	if _, err := normalizeFreeProxyImportOptions(importFreeProxiesRequest{
		Mode:                           schedule.Mode,
		Group:                          schedule.Group,
		Limit:                          schedule.Limit,
		Concurrency:                    schedule.Concurrency,
		TimeoutSeconds:                 schedule.TimeoutSeconds,
		RetryCount:                     schedule.RetryCount,
		CleanupEnabled:                 schedule.CleanupEnabled,
		CleanupMaxLatencyMs:            schedule.CleanupMaxLatencyMs,
		CleanupDeleteFailedAutoProxies: schedule.CleanupDeleteFailedAutoProxies,
	}); err != nil {
		return models.ProxyImportSchedule{}, err
	}
	return schedule, nil
}

func normalizeProxyImportScheduleTimes(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	items := make([]string, 0)
	for _, value := range values {
		parts := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r'
		})
		for _, part := range parts {
			_, _, normalized, err := parseProxyImportClock(part)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			items = append(items, normalized)
		}
	}
	sort.Strings(items)
	return items, nil
}

func parseProxyImportClock(value string) (int, int, string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, 0, "", nil
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		return 0, 0, "", fmt.Errorf("无效的时间格式: %s，必须是 HH:MM", trimmed)
	}
	hour := parseClockPart(parts[0])
	minute := parseClockPart(parts[1])
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, "", fmt.Errorf("无效的时间: %s，必须在 00:00-23:59 之间", trimmed)
	}
	return hour, minute, fmt.Sprintf("%02d:%02d", hour, minute), nil
}

func parseClockPart(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return -1
	}
	result := 0
	for _, ch := range trimmed {
		if ch < '0' || ch > '9' {
			return -1
		}
		result = result*10 + int(ch-'0')
	}
	return result
}

func shouldTriggerProxyImportSchedule(schedule models.ProxyImportSchedule, now time.Time) bool {
	if !schedule.Enabled || len(schedule.Times) == 0 {
		return false
	}
	current := now.Format("15:04")
	matched := false
	for _, item := range schedule.Times {
		if item == current {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	if schedule.LastRunAt.IsZero() {
		return true
	}
	last := schedule.LastRunAt.In(now.Location())
	return !(last.Year() == now.Year() && last.Month() == now.Month() && last.Day() == now.Day() && last.Hour() == now.Hour() && last.Minute() == now.Minute())
}

func computeNextProxyImportRun(schedule models.ProxyImportSchedule, now time.Time) *time.Time {
	if !schedule.Enabled || len(schedule.Times) == 0 {
		return nil
	}
	var next time.Time
	for dayOffset := 0; dayOffset <= 7; dayOffset++ {
		date := now.AddDate(0, 0, dayOffset)
		for _, item := range schedule.Times {
			hour, minute, _, err := parseProxyImportClock(item)
			if err != nil {
				continue
			}
			candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, now.Location())
			if !candidate.After(now) {
				continue
			}
			if next.IsZero() || candidate.Before(next) {
				next = candidate
			}
		}
		if !next.IsZero() {
			break
		}
	}
	if next.IsZero() {
		return nil
	}
	return &next
}

func buildProxyImportMessage(summary freeProxyImportSummary, trigger string) string {
	prefix := "自动抓取完成"
	if trigger == proxyImportTriggerScheduled {
		prefix = "定时抓取完成"
	}
	if summary.CandidateCount == 0 {
		return prefix + "：没有抓到任何候选代理，请稍后重试。"
	}
	if summary.AvailableCount == 0 {
		return fmt.Sprintf("%s：候选 %d 个，已全部测速，但没有可用代理。", prefix, summary.CandidateCount)
	}
	return fmt.Sprintf("%s：候选 %d 个，测试 %d 个，可用 %d 个，新增 %d 个，更新 %d 个，命中手动代理 %d 个。", prefix, summary.CandidateCount, summary.TestedCount, summary.AvailableCount, summary.ImportedCount, summary.UpdatedCount, summary.MatchedManualCount)
}

func scaleProgress(done, total, start, end int) int {
	if total <= 0 {
		return end
	}
	if done <= 0 {
		return start
	}
	if done >= total {
		return end
	}
	return start + int(float64(end-start)*float64(done)/float64(total))
}

func clampProxyImportProgress(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *ProxyImportManager) appendExecutionLog(task proxyImportTaskState) {
	if task.ID == "" || task.Status == proxyImportStatusRunning {
		return
	}
	_ = db.UpdateStore(func(store *db.Store) error {
		entry := models.ProxyImportExecutionLog{
			TaskID:                         task.ID,
			Trigger:                        task.Trigger,
			Status:                         task.Status,
			Message:                        task.Message,
			Mode:                           task.Request.Mode,
			Group:                          task.Request.Group,
			Limit:                          task.Request.Limit,
			Concurrency:                    task.Request.Concurrency,
			TimeoutSeconds:                 task.Request.TimeoutSeconds,
			RetryCount:                     task.Request.RetryCount,
			CleanupEnabled:                 task.Request.CleanupEnabled,
			CleanupMaxLatencyMs:            task.Request.CleanupMaxLatencyMs,
			CleanupDeleteFailedAutoProxies: task.Request.CleanupDeleteFailedAutoProxies,
			StartedAt:                      task.StartedAt,
			FinishedAt:                     task.FinishedAt,
			CandidateCount:                 task.CandidateCount,
			TestedCount:                    task.TestedCount,
			AvailableCount:                 task.AvailableCount,
			FailedCount:                    task.FailedCount,
			ImportedCount:                  task.ImportedCount,
			UpdatedCount:                   task.UpdatedCount,
			MatchedManualCount:             task.MatchedManual,
			CleanedSlowCount:               task.CleanedSlowCount,
			CleanedFailedCount:             task.CleanedFailedCount,
			CleanupDeletedCount:            task.CleanupDeletedCount,
			UnboundKeyCount:                task.UnboundKeyCount,
		}
		if task.Summary != nil {
			entry.SourceErrorCount = task.Summary.SourceErrorCount
		}
		store.ProxyImportLogs = append([]models.ProxyImportExecutionLog{entry}, store.ProxyImportLogs...)
		if len(store.ProxyImportLogs) > 20 {
			store.ProxyImportLogs = store.ProxyImportLogs[:20]
		}
		return nil
	})
}
