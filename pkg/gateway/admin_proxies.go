package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"

	"github.com/gofiber/fiber/v2"
)

type proxyTestRecordResponse struct {
	Success      bool      `json:"success"`
	StatusCode   int       `json:"statusCode,omitempty"`
	ResponseTime int64     `json:"responseTime,omitempty"`
	Message      string    `json:"message,omitempty"`
	Target       string    `json:"target,omitempty"`
	TestedAt     time.Time `json:"testedAt"`
	Summary      string    `json:"summary"`
}

type upstreamProxyResponse struct {
	ID            uint                      `json:"id"`
	Name          string                    `json:"name"`
	Group         string                    `json:"group,omitempty"`
	Country       string                    `json:"country,omitempty"`
	Source        string                    `json:"source"`
	ManagedBy     string                    `json:"managedBy,omitempty"`
	ManagedRefID  uint                      `json:"managedRefId,omitempty"`
	Type          string                    `json:"type"`
	Status        string                    `json:"status"`
	Host          string                    `json:"host"`
	Port          int                       `json:"port"`
	Username      string                    `json:"username,omitempty"`
	HasPassword   bool                      `json:"hasPassword"`
	BoundKeyCount int                       `json:"boundKeyCount"`
	URLPreview    string                    `json:"urlPreview"`
	LastTest      *proxyTestRecordResponse  `json:"lastTest,omitempty"`
	TestHistory   []proxyTestRecordResponse `json:"testHistory,omitempty"`
	CreatedAt     time.Time                 `json:"createdAt"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
}

type upstreamProxiesResponse struct {
	Proxies []upstreamProxyResponse `json:"proxies"`
}

type upstreamProxyOptionResponse struct {
	ID         uint                     `json:"id"`
	Name       string                   `json:"name"`
	Group      string                   `json:"group,omitempty"`
	Source     string                   `json:"source,omitempty"`
	ManagedBy  string                   `json:"managedBy,omitempty"`
	Status     string                   `json:"status"`
	Type       string                   `json:"type"`
	ProxyURL   string                   `json:"proxyURL"`
	URLPreview string                   `json:"urlPreview"`
	LastTest   *proxyTestRecordResponse `json:"lastTest,omitempty"`
}

type upstreamProxyOptionsResponse struct {
	Options []upstreamProxyOptionResponse `json:"options"`
}

type createUpstreamProxyRequest struct {
	Name     string `json:"name"`
	Group    string `json:"group,omitempty"`
	Country  string `json:"country,omitempty"`
	Type     string `json:"type"`
	Status   string `json:"status,omitempty"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type updateUpstreamProxyRequest struct {
	Name     *string `json:"name"`
	Group    *string `json:"group,omitempty"`
	Country  *string `json:"country,omitempty"`
	Type     *string `json:"type"`
	Status   *string `json:"status,omitempty"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

type testUpstreamProxyRequest struct {
	ProxyID  *uint  `json:"proxyId,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type updateUpstreamProxyStatusRequest struct {
	Status string `json:"status"`
}

type importFreeProxiesRequest struct {
	Mode                           string `json:"mode,omitempty"`
	Group                          string `json:"group,omitempty"`
	Limit                          int    `json:"limit,omitempty"`
	Concurrency                    int    `json:"concurrency,omitempty"`
	TimeoutSeconds                 int    `json:"timeoutSeconds,omitempty"`
	RetryCount                     int    `json:"retryCount,omitempty"`
	CleanupEnabled                 bool   `json:"cleanupEnabled,omitempty"`
	CleanupMaxLatencyMs            int    `json:"cleanupMaxLatencyMs,omitempty"`
	CleanupDeleteFailedAutoProxies bool   `json:"cleanupDeleteFailedAutoProxies,omitempty"`
}

type externalProxySourceCounts struct {
	HTTPTXT    int `json:"httpTxt"`
	HTTPJSON   int `json:"httpJSON"`
	HTTPHTML   int `json:"httpHTML"`
	SOCKS5TXT  int `json:"socks5Txt"`
	SOCKS5JSON int `json:"socks5JSON"`
	SOCKS5HTML int `json:"socks5HTML"`
	Total      int `json:"total"`
}

type externalProxySourcesResponse struct {
	Sources   models.ExternalProxySources `json:"sources"`
	Builtin   externalProxySourceCounts   `json:"builtin"`
	External  externalProxySourceCounts   `json:"external"`
	Effective externalProxySourceCounts   `json:"effective"`
}

type bulkUpdateUpstreamProxyStatusRequest struct {
	IDs    []uint `json:"ids"`
	Status string `json:"status"`
}

type bulkProxyIDsRequest struct {
	IDs []uint `json:"ids"`
}

var errProxyNotFound = errors.New("代理不存在")
var errManagedProxyReadOnly = errors.New("托管代理请在 Xray 节点中管理")

func GetUpstreamProxies(c *fiber.Ctx) error {
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "读取代理失败"})
	}
	return c.JSON(upstreamProxiesResponse{Proxies: buildProxyResponses(store.Proxies, store.APIKeys)})
}

func GetUpstreamProxyOptions(c *fiber.Ctx) error {
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "读取代理失败"})
	}
	options := make([]upstreamProxyOptionResponse, 0, len(store.Proxies))
	for _, proxy := range store.Proxies {
		proxy = models.NormalizeUpstreamProxy(proxy)
		proxyURL, buildErr := buildProxyURLFromModel(proxy)
		if buildErr != nil {
			continue
		}
		options = append(options, upstreamProxyOptionResponse{
			ID:         proxy.ID,
			Name:       proxy.Name,
			Group:      proxy.Group,
			Source:     proxy.Source,
			ManagedBy:  proxy.ManagedBy,
			Status:     proxy.Status,
			Type:       proxy.Type,
			ProxyURL:   proxyURL,
			URLPreview: buildProxyPreviewFromModel(proxy),
			LastTest:   buildProxyTestResponse(proxy.LastTest),
		})
	}
	sort.SliceStable(options, func(i, j int) bool {
		rank := func(item upstreamProxyOptionResponse) int {
			if item.Status != models.ProxyStatusEnabled {
				return 2
			}
			if item.ManagedBy == models.CoreManagedByXray {
				return 0
			}
			return 1
		}
		if rank(options[i]) != rank(options[j]) {
			return rank(options[i]) < rank(options[j])
		}
		if options[i].Group != options[j].Group {
			return options[i].Group < options[j].Group
		}
		return options[i].Name < options[j].Name
	})
	return c.JSON(upstreamProxyOptionsResponse{Options: options})
}

func GetExternalProxySources(c *fiber.Ctx) error {
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "读取外置代理源配置失败"})
	}
	sources := models.NormalizeExternalProxySources(store.ExternalProxySources)
	return c.JSON(buildExternalProxySourcesResponse(sources))
}

func UpdateExternalProxySources(c *fiber.Ctx) error {
	var req models.ExternalProxySources
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
	}
	normalized := models.NormalizeExternalProxySources(req)
	if err := db.UpdateStore(func(store *db.Store) error {
		store.ExternalProxySources = normalized
		return nil
	}); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "保存外置代理源配置失败"})
	}
	return c.JSON(fiber.Map{
		"message": "外置代理源配置更新成功",
		"config":  buildExternalProxySourcesResponse(normalized),
	})
}

func AddUpstreamProxy(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req createUpstreamProxyRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		password, err := encryptProxyPassword(req.Password)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		now := time.Now()
		proxyCfg := models.NormalizeUpstreamProxy(models.UpstreamProxy{
			Name:        req.Name,
			Group:       req.Group,
			Country:     req.Country,
			Type:        req.Type,
			Status:      req.Status,
			Host:        req.Host,
			Port:        req.Port,
			Username:    req.Username,
			Password:    password,
			TestHistory: make([]models.ProxyTestRecord, 0),
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		if err := validateUpstreamProxyModel(proxyCfg); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if err := db.UpdateStore(func(store *db.Store) error {
			proxyCfg.ID = store.NextProxyID
			store.NextProxyID++
			store.Proxies = append(store.Proxies, proxyCfg)
			return nil
		}); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "保存代理失败"})
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "代理已保存，但刷新运行时失败"})
		}
		return c.JSON(fiber.Map{"message": "代理添加成功", "proxy": newProxyResponse(proxyCfg, 0)})
	}
}

func UpdateUpstreamProxy(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseProxyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		var req updateUpstreamProxyRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		updated, err := mutateProxy(id, func(proxyCfg *models.UpstreamProxy) error {
			if strings.TrimSpace(proxyCfg.ManagedBy) != "" {
				return errManagedProxyReadOnly
			}
			if req.Name != nil {
				proxyCfg.Name = strings.TrimSpace(*req.Name)
			}
			if req.Group != nil {
				proxyCfg.Group = strings.TrimSpace(*req.Group)
			}
			if req.Type != nil {
				proxyCfg.Type = strings.TrimSpace(*req.Type)
			}
			if req.Status != nil {
				proxyCfg.Status = strings.TrimSpace(*req.Status)
			}
			if req.Host != nil {
				proxyCfg.Host = strings.TrimSpace(*req.Host)
			}
			if req.Port != nil {
				proxyCfg.Port = *req.Port
			}
			if req.Username != nil {
				proxyCfg.Username = strings.TrimSpace(*req.Username)
			}
			if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
				encrypted, encryptErr := encryptProxyPassword(*req.Password)
				if encryptErr != nil {
					return encryptErr
				}
				proxyCfg.Password = encrypted
			}
			*proxyCfg = models.NormalizeUpstreamProxy(*proxyCfg)
			if err := validateUpstreamProxyModel(*proxyCfg); err != nil {
				return err
			}
			proxyCfg.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			status := 500
			if errors.Is(err, errProxyNotFound) {
				status = 404
			} else {
				status = 400
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "代理已更新，但刷新运行时失败"})
		}
		return c.JSON(fiber.Map{"message": "代理更新成功", "proxy": newProxyResponse(updated, 0)})
	}
}

func UpdateUpstreamProxyStatus(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseProxyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		var req updateUpstreamProxyStatusRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		status := strings.TrimSpace(req.Status)
		if err := validateUpstreamProxyStatus(status); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		updated, err := mutateProxy(id, func(proxyCfg *models.UpstreamProxy) error {
			if strings.TrimSpace(proxyCfg.ManagedBy) != "" {
				return errManagedProxyReadOnly
			}
			proxyCfg.Status = status
			proxyCfg.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			code := 500
			if errors.Is(err, errProxyNotFound) {
				code = 404
			}
			return c.Status(code).JSON(fiber.Map{"error": err.Error()})
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "代理状态已更新，但刷新运行时失败"})
		}
		return c.JSON(fiber.Map{"message": "代理状态更新成功", "proxy": newProxyResponse(updated, 0)})
	}
}

func BulkUpdateUpstreamProxyStatus(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req bulkUpdateUpstreamProxyStatusRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		if len(req.IDs) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "至少选择一个代理"})
		}
		status := strings.TrimSpace(req.Status)
		if err := validateUpstreamProxyStatus(status); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		idSet := make(map[uint]struct{}, len(req.IDs))
		for _, id := range req.IDs {
			if id > 0 {
				idSet[id] = struct{}{}
			}
		}
		if len(idSet) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "代理 ID 无效"})
		}
		updatedCount := 0
		now := time.Now()
		if err := db.UpdateStore(func(store *db.Store) error {
			for i := range store.Proxies {
				if _, ok := idSet[store.Proxies[i].ID]; !ok {
					continue
				}
				store.Proxies[i].Status = status
				store.Proxies[i].UpdatedAt = now
				updatedCount++
			}
			return nil
		}); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "批量更新代理状态失败"})
		}
		if updatedCount == 0 {
			return c.Status(404).JSON(fiber.Map{"error": "未找到匹配的代理"})
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "批量更新成功，但刷新运行时失败"})
		}
		return c.JSON(fiber.Map{"message": fmt.Sprintf("已批量更新 %d 个代理。", updatedCount), "updatedCount": updatedCount})
	}
}

func ExportUpstreamProxies(c *fiber.Ctx) error {
	var req bulkProxyIDsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
	}
	if len(req.IDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "至少选择一个代理"})
	}
	idSet := make(map[uint]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id > 0 {
			idSet[id] = struct{}{}
		}
	}
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "读取代理失败"})
	}
	lines := make([]string, 0, len(idSet))
	for _, proxyCfg := range store.Proxies {
		if _, ok := idSet[proxyCfg.ID]; !ok {
			continue
		}
		url, buildErr := buildProxyURLFromModel(proxyCfg)
		if buildErr != nil {
			url = buildProxyPreviewFromModel(proxyCfg)
		}
		lines = append(lines, url)
	}
	if len(lines) == 0 {
		return c.Status(404).JSON(fiber.Map{"error": "未找到匹配的代理"})
	}
	return c.JSON(fiber.Map{"content": strings.Join(lines, "\n"), "count": len(lines)})
}

func deleteManualUpstreamProxiesByIDs(ids []uint) (int, int, error) {
	idSet := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			idSet[id] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return 0, 0, fmt.Errorf("代理 ID 无效")
	}
	deletedCount := 0
	unboundCount := 0
	now := time.Now()
	err := db.UpdateStore(func(store *db.Store) error {
		kept := make([]models.UpstreamProxy, 0, len(store.Proxies))
		for _, proxyCfg := range store.Proxies {
			if _, ok := idSet[proxyCfg.ID]; ok {
				if strings.TrimSpace(proxyCfg.ManagedBy) != "" {
					return errManagedProxyReadOnly
				}
				deletedCount++
				continue
			}
			kept = append(kept, proxyCfg)
		}
		store.Proxies = kept
		for i := range store.APIKeys {
			if _, ok := idSet[store.APIKeys[i].ProxyID]; ok {
				store.APIKeys[i].ProxyID = 0
				store.APIKeys[i].UpdatedAt = now
				unboundCount++
			}
		}
		if deletedCount == 0 {
			return errProxyNotFound
		}
		return nil
	})
	return deletedCount, unboundCount, err
}

func BulkDeleteUpstreamProxies(sched *scheduler.Scheduler, manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req bulkProxyIDsRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		if len(req.IDs) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "至少选择一个代理"})
		}
		idSet := make(map[uint]struct{}, len(req.IDs))
		for _, id := range req.IDs {
			if id > 0 {
				idSet[id] = struct{}{}
			}
		}
		if len(idSet) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "代理 ID 无效"})
		}

		store, err := db.ReadStore()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "读取代理失败"})
		}
		manualIDs := make([]uint, 0, len(idSet))
		coreIDs := make([]uint, 0)
		seenCoreIDs := make(map[uint]struct{})
		for _, proxyCfg := range store.Proxies {
			if _, ok := idSet[proxyCfg.ID]; !ok {
				continue
			}
			if strings.TrimSpace(proxyCfg.ManagedBy) == models.CoreManagedByXray && proxyCfg.ManagedRefID > 0 {
				if _, exists := seenCoreIDs[proxyCfg.ManagedRefID]; !exists {
					seenCoreIDs[proxyCfg.ManagedRefID] = struct{}{}
					coreIDs = append(coreIDs, proxyCfg.ManagedRefID)
				}
				continue
			}
			manualIDs = append(manualIDs, proxyCfg.ID)
		}
		if len(manualIDs) == 0 && len(coreIDs) == 0 {
			return c.Status(404).JSON(fiber.Map{"error": "未找到匹配的代理"})
		}

		deletedCount := 0
		deletedManagedProxyCount := 0
		deletedCoreCount := 0
		unboundCount := 0
		if len(manualIDs) > 0 {
			manualDeleted, manualUnbound, err := deleteManualUpstreamProxiesByIDs(manualIDs)
			if err != nil {
				status := 500
				if errors.Is(err, errProxyNotFound) {
					status = 404
				}
				return c.Status(status).JSON(fiber.Map{"error": err.Error()})
			}
			deletedCount += manualDeleted
			unboundCount += manualUnbound
		}
		if len(coreIDs) > 0 {
			coreDeleted, managedDeleted, coreUnbound, err := deleteCoreProfilesByIDs(coreIDs)
			if err != nil {
				status := 500
				if errors.Is(err, errCoreProfileNotFound) {
					status = 404
				}
				return c.Status(status).JSON(fiber.Map{"error": err.Error()})
			}
			deletedCount += managedDeleted
			deletedManagedProxyCount += managedDeleted
			deletedCoreCount += coreDeleted
			unboundCount += coreUnbound
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "批量删除成功，但刷新运行时失败"})
		}
		if manager != nil && len(coreIDs) > 0 {
			if err := manager.Reload(context.Background()); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "代理已删除，但重载 Xray 失败: " + err.Error()})
			}
		}
		message := fmt.Sprintf("已批量删除 %d 个代理，自动解绑 %d 个 key。", deletedCount, unboundCount)
		if deletedCoreCount > 0 {
			message += fmt.Sprintf(" 同时删除 %d 个关联核心节点。", deletedCoreCount)
		}
		return c.JSON(fiber.Map{
			"message":                  message,
			"deletedCount":             deletedCount,
			"deletedManagedProxyCount": deletedManagedProxyCount,
			"deletedCoreCount":         deletedCoreCount,
			"unboundCount":             unboundCount,
		})
	}
}

func DeleteUpstreamProxy(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseProxyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if err := db.UpdateStore(func(store *db.Store) error {
			for i := range store.Proxies {
				if store.Proxies[i].ID != id {
					continue
				}
				if strings.TrimSpace(store.Proxies[i].ManagedBy) != "" {
					return errManagedProxyReadOnly
				}
				store.Proxies = append(store.Proxies[:i], store.Proxies[i+1:]...)
				for j := range store.APIKeys {
					if store.APIKeys[j].ProxyID == id {
						store.APIKeys[j].ProxyID = 0
						store.APIKeys[j].UpdatedAt = time.Now()
					}
				}
				return nil
			}
			return errProxyNotFound
		}); err != nil {
			status := 500
			if errors.Is(err, errProxyNotFound) {
				status = 404
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "代理已删除，但刷新运行时失败"})
		}
		return c.JSON(fiber.Map{"message": "代理删除成功，相关上游 key 已自动解绑"})
	}
}

func TestUpstreamProxy(c *fiber.Ctx) error {
	var req testUpstreamProxyRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
	}
	var proxyCfg models.UpstreamProxy
	persistToStore := uint(0)
	if req.ProxyID != nil && *req.ProxyID > 0 {
		store, err := db.ReadStore()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "读取代理失败"})
		}
		found := false
		for _, item := range store.Proxies {
			if item.ID == *req.ProxyID {
				proxyCfg = item
				persistToStore = item.ID
				found = true
				break
			}
		}
		if !found {
			return c.Status(404).JSON(fiber.Map{"error": errProxyNotFound.Error()})
		}
	} else {
		password, err := encryptProxyPassword(req.Password)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		proxyCfg = models.NormalizeUpstreamProxy(models.UpstreamProxy{
			Name:     req.Name,
			Group:    "",
			Type:     req.Type,
			Status:   models.ProxyStatusEnabled,
			Host:     req.Host,
			Port:     req.Port,
			Username: req.Username,
			Password: password,
		})
	}
	if err := validateUpstreamProxyModel(proxyCfg); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	result := testUpstreamProxyConnectivity(context.Background(), proxyCfg)
	if persistToStore > 0 {
		record := buildProxyTestRecordFromResult(result)
		if err := recordProxyTestResult(persistToStore, record); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "代理测试完成，但保存测试历史失败: " + err.Error()})
		}
	}
	return c.JSON(result)
}

func ImportFreeProxies(manager *ProxyImportManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req importFreeProxiesRequest
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&req); err != nil {
				return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
			}
		}
		task, err := manager.StartManualImport(req)
		if err != nil {
			status := 400
			if errors.Is(err, errProxyImportAlreadyRunning) {
				status = 409
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error(), "task": task})
		}
		return c.Status(202).JSON(fiber.Map{"message": "自动抓取任务已转入后台执行。", "task": task})
	}
}

func parseProxyID(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("无效的代理 ID")
	}
	return uint(id), nil
}

func mutateProxy(id uint, mutator func(*models.UpstreamProxy) error) (models.UpstreamProxy, error) {
	var updated models.UpstreamProxy
	err := db.UpdateStore(func(store *db.Store) error {
		for i := range store.Proxies {
			if store.Proxies[i].ID != id {
				continue
			}
			if err := mutator(&store.Proxies[i]); err != nil {
				return err
			}
			updated = store.Proxies[i]
			return nil
		}
		return errProxyNotFound
	})
	return updated, err
}

func buildProxyResponses(proxies []models.UpstreamProxy, keys []models.APIKey) []upstreamProxyResponse {
	counts := countAPIKeyProxyUsage(keys)
	items := make([]upstreamProxyResponse, 0, len(proxies))
	for _, proxyCfg := range proxies {
		items = append(items, newProxyResponse(proxyCfg, counts[proxyCfg.ID]))
	}
	return items
}

func buildProxyTestResponse(record *models.ProxyTestRecord) *proxyTestRecordResponse {
	if record == nil {
		return nil
	}
	return &proxyTestRecordResponse{
		Success:      record.Success,
		StatusCode:   record.StatusCode,
		ResponseTime: record.ResponseTime,
		Message:      record.Message,
		Target:       record.Target,
		TestedAt:     record.TestedAt,
		Summary:      formatProxyHistoryLabel(*record),
	}
}

func buildProxyTestHistoryResponses(history []models.ProxyTestRecord) []proxyTestRecordResponse {
	items := make([]proxyTestRecordResponse, 0, len(history))
	for _, item := range history {
		copyRecord := item
		resp := buildProxyTestResponse(&copyRecord)
		if resp != nil {
			items = append(items, *resp)
		}
	}
	return items
}

func countExternalProxySources(sources models.ExternalProxySources) externalProxySourceCounts {
	sources = models.NormalizeExternalProxySources(sources)
	counts := externalProxySourceCounts{
		HTTPTXT:    len(sources.HTTPTXT),
		HTTPJSON:   len(sources.HTTPJSON),
		HTTPHTML:   len(sources.HTTPHTML),
		SOCKS5TXT:  len(sources.SOCKS5TXT),
		SOCKS5JSON: len(sources.SOCKS5JSON),
		SOCKS5HTML: len(sources.SOCKS5HTML),
	}
	counts.Total = counts.HTTPTXT + counts.HTTPJSON + counts.HTTPHTML + counts.SOCKS5TXT + counts.SOCKS5JSON + counts.SOCKS5HTML
	return counts
}

func builtinExternalProxySourceCounts() externalProxySourceCounts {
	return externalProxySourceCounts{
		HTTPTXT:    len(freeProxyHTTPSourcesTXT),
		HTTPJSON:   len(freeProxyHTTPSourcesJSON),
		HTTPHTML:   len(freeProxyHTTPSourcesHTML),
		SOCKS5TXT:  len(freeProxySOCKS5SourcesTXT),
		SOCKS5JSON: len(freeProxySOCKS5SourcesJSON),
		SOCKS5HTML: len(freeProxySOCKS5SourcesHTML),
		Total:      len(freeProxyHTTPSourcesTXT) + len(freeProxyHTTPSourcesJSON) + len(freeProxyHTTPSourcesHTML) + len(freeProxySOCKS5SourcesTXT) + len(freeProxySOCKS5SourcesJSON) + len(freeProxySOCKS5SourcesHTML),
	}
}

func buildExternalProxySourcesResponse(sources models.ExternalProxySources) externalProxySourcesResponse {
	sources = models.NormalizeExternalProxySources(sources)
	builtin := builtinExternalProxySourceCounts()
	external := countExternalProxySources(sources)
	effective := countExternalProxySources(models.ExternalProxySources{
		HTTPTXT:    append(append([]string{}, freeProxyHTTPSourcesTXT...), sources.HTTPTXT...),
		HTTPJSON:   append(append([]string{}, freeProxyHTTPSourcesJSON...), sources.HTTPJSON...),
		HTTPHTML:   append(append([]string{}, freeProxyHTTPSourcesHTML...), sources.HTTPHTML...),
		SOCKS5TXT:  append(append([]string{}, freeProxySOCKS5SourcesTXT...), sources.SOCKS5TXT...),
		SOCKS5JSON: append(append([]string{}, freeProxySOCKS5SourcesJSON...), sources.SOCKS5JSON...),
		SOCKS5HTML: append(append([]string{}, freeProxySOCKS5SourcesHTML...), sources.SOCKS5HTML...),
	})
	return externalProxySourcesResponse{
		Sources:   sources,
		Builtin:   builtin,
		External:  external,
		Effective: effective,
	}
}

func newProxyResponse(proxyCfg models.UpstreamProxy, boundCount int) upstreamProxyResponse {
	proxyCfg = models.NormalizeUpstreamProxy(proxyCfg)
	return upstreamProxyResponse{
		ID:            proxyCfg.ID,
		Name:          proxyCfg.Name,
		Group:         proxyCfg.Group,
		Country:       proxyCfg.Country,
		Source:        proxyCfg.Source,
		ManagedBy:     proxyCfg.ManagedBy,
		ManagedRefID:  proxyCfg.ManagedRefID,
		Type:          proxyCfg.Type,
		Status:        proxyCfg.Status,
		Host:          proxyCfg.Host,
		Port:          proxyCfg.Port,
		Username:      proxyCfg.Username,
		HasPassword:   strings.TrimSpace(proxyCfg.Password) != "",
		BoundKeyCount: boundCount,
		URLPreview:    buildProxyPreviewFromModel(proxyCfg),
		LastTest:      buildProxyTestResponse(proxyCfg.LastTest),
		TestHistory:   buildProxyTestHistoryResponses(proxyCfg.TestHistory),
		CreatedAt:     proxyCfg.CreatedAt,
		UpdatedAt:     proxyCfg.UpdatedAt,
	}
}
