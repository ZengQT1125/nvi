package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"

	"github.com/gofiber/fiber/v2"
)

type coreProfileResponse struct {
	ID               uint                     `json:"id"`
	Name             string                   `json:"name"`
	Protocol         string                   `json:"protocol"`
	Status           string                   `json:"status"`
	Server           string                   `json:"server"`
	Port             int                      `json:"port"`
	LocalPort        int                      `json:"localPort"`
	LocalProxyURL    string                   `json:"localProxyURL"`
	ManagedProxyID   uint                     `json:"managedProxyId,omitempty"`
	Transport        string                   `json:"transport,omitempty"`
	TLSMode          string                   `json:"tlsMode,omitempty"`
	SNI              string                   `json:"sni,omitempty"`
	AllowInsecure    bool                     `json:"allowInsecure,omitempty"`
	Host             string                   `json:"host,omitempty"`
	Path             string                   `json:"path,omitempty"`
	ServiceName      string                   `json:"serviceName,omitempty"`
	Flow             string                   `json:"flow,omitempty"`
	Method           string                   `json:"method,omitempty"`
	Username         string                   `json:"username,omitempty"`
	HasPassword      bool                     `json:"hasPassword"`
	HasAuthID        bool                     `json:"hasAuthId"`
	Fingerprint      string                   `json:"fingerprint,omitempty"`
	RealityPublicKey string                   `json:"realityPublicKey,omitempty"`
	RealityShortID   string                   `json:"realityShortId,omitempty"`
	RealitySpiderX   string                   `json:"realitySpiderX,omitempty"`
	Remarks          string                   `json:"remarks,omitempty"`
	LastTest         *proxyTestRecordResponse `json:"lastTest,omitempty"`
	CreatedAt        time.Time                `json:"createdAt"`
	UpdatedAt        time.Time                `json:"updatedAt"`
}

type coreProfilesResponse struct {
	Profiles []coreProfileResponse `json:"profiles"`
	Runtime  xrayRuntimeSnapshot   `json:"runtime"`
}

type createCoreProfileRequest struct {
	Name             string `json:"name"`
	Protocol         string `json:"protocol"`
	Status           string `json:"status,omitempty"`
	Server           string `json:"server"`
	Port             int    `json:"port"`
	LocalPort        int    `json:"localPort,omitempty"`
	Transport        string `json:"transport,omitempty"`
	TLSMode          string `json:"tlsMode,omitempty"`
	SNI              string `json:"sni,omitempty"`
	AllowInsecure    bool   `json:"allowInsecure,omitempty"`
	Host             string `json:"host,omitempty"`
	Path             string `json:"path,omitempty"`
	ServiceName      string `json:"serviceName,omitempty"`
	Flow             string `json:"flow,omitempty"`
	Method           string `json:"method,omitempty"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"`
	AuthID           string `json:"authId,omitempty"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	RealityPublicKey string `json:"realityPublicKey,omitempty"`
	RealityShortID   string `json:"realityShortId,omitempty"`
	RealitySpiderX   string `json:"realitySpiderX,omitempty"`
	Remarks          string `json:"remarks,omitempty"`
}

type updateCoreProfileRequest struct {
	Name             *string `json:"name"`
	Protocol         *string `json:"protocol"`
	Status           *string `json:"status,omitempty"`
	Server           *string `json:"server"`
	Port             *int    `json:"port"`
	LocalPort        *int    `json:"localPort,omitempty"`
	Transport        *string `json:"transport,omitempty"`
	TLSMode          *string `json:"tlsMode,omitempty"`
	SNI              *string `json:"sni,omitempty"`
	AllowInsecure    *bool   `json:"allowInsecure,omitempty"`
	Host             *string `json:"host,omitempty"`
	Path             *string `json:"path,omitempty"`
	ServiceName      *string `json:"serviceName,omitempty"`
	Flow             *string `json:"flow,omitempty"`
	Method           *string `json:"method,omitempty"`
	Username         *string `json:"username,omitempty"`
	Password         *string `json:"password,omitempty"`
	AuthID           *string `json:"authId,omitempty"`
	Fingerprint      *string `json:"fingerprint,omitempty"`
	RealityPublicKey *string `json:"realityPublicKey,omitempty"`
	RealityShortID   *string `json:"realityShortId,omitempty"`
	RealitySpiderX   *string `json:"realitySpiderX,omitempty"`
	Remarks          *string `json:"remarks,omitempty"`
}

type updateCoreProfileStatusRequest struct {
	Status string `json:"status"`
}

type batchTestCoreProfilesRequest struct {
	IDs []uint `json:"ids"`
}

type batchTestCoreProfilesItem struct {
	ID           uint                 `json:"id"`
	Name         string               `json:"name"`
	Success      bool                 `json:"success"`
	StatusCode   int                  `json:"statusCode,omitempty"`
	ResponseTime int                  `json:"responseTime,omitempty"`
	Message      string               `json:"message,omitempty"`
	Summary      string               `json:"summary,omitempty"`
	Target       string               `json:"target,omitempty"`
	Profile      *coreProfileResponse `json:"profile,omitempty"`
}

type batchTestCoreProfilesResponse struct {
	Message      string                      `json:"message"`
	Total        int                         `json:"total"`
	SuccessCount int                         `json:"successCount"`
	FailedCount  int                         `json:"failedCount"`
	Target       string                      `json:"target,omitempty"`
	Results      []batchTestCoreProfilesItem `json:"results"`
	Runtime      xrayRuntimeSnapshot         `json:"runtime"`
}

var errCoreProfileNotFound = errors.New("核心节点不存在")

func GetCoreProfiles(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		store, err := db.ReadStore()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "读取核心节点失败"})
		}
		items := make([]coreProfileResponse, 0, len(store.CoreProfiles))
		for _, item := range store.CoreProfiles {
			items = append(items, newCoreProfileResponse(item))
		}
		return c.JSON(coreProfilesResponse{Profiles: items, Runtime: manager.Snapshot()})
	}
}

func CreateCoreProfile(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req createCoreProfileRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		profile, err := buildCoreProfileFromCreate(req)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if err := db.UpdateStore(func(store *db.Store) error {
			profile.ID = store.NextCoreProfileID
			store.NextCoreProfileID++
			store.CoreProfiles = append(store.CoreProfiles, profile)
			return nil
		}); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "保存核心节点失败"})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "核心节点已保存，但重载 Xray 失败: " + err.Error()})
		}
		created, _ := getCoreProfileByID(profile.ID)
		return c.JSON(fiber.Map{"message": "核心节点添加成功", "profile": newCoreProfileResponse(created), "runtime": manager.Snapshot()})
	}
}

func UpdateCoreProfile(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseCoreProfileID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		var req updateCoreProfileRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		updated, err := mutateCoreProfile(id, func(profile *models.CoreProfile) error {
			return applyCoreProfileUpdate(profile, req)
		})
		if err != nil {
			status := 500
			if errors.Is(err, errCoreProfileNotFound) {
				status = 404
			} else {
				status = 400
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "核心节点已更新，但重载 Xray 失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{"message": "核心节点更新成功", "profile": newCoreProfileResponse(updated), "runtime": manager.Snapshot()})
	}
}

func DeleteCoreProfile(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseCoreProfileID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		deletedCount, deletedProxyCount, unboundCount, err := deleteCoreProfilesByIDs([]uint{id})
		if err != nil {
			status := 500
			if errors.Is(err, errCoreProfileNotFound) {
				status = 404
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "核心节点已删除，但重载 Xray 失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{
			"message":           fmt.Sprintf("已删除 %d 个核心节点，移除 %d 个托管代理，自动解绑 %d 个 key。", deletedCount, deletedProxyCount, unboundCount),
			"deletedCount":      deletedCount,
			"deletedProxyCount": deletedProxyCount,
			"unboundCount":      unboundCount,
			"runtime":           manager.Snapshot(),
		})
	}
}

func BulkDeleteCoreProfiles(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req batchTestCoreProfilesRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		ids := normalizeCoreProfileIDs(req.IDs)
		if len(ids) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "至少选择一个核心节点"})
		}
		deletedCount, deletedProxyCount, unboundCount, err := deleteCoreProfilesByIDs(ids)
		if err != nil {
			status := 500
			if errors.Is(err, errCoreProfileNotFound) {
				status = 404
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "核心节点已删除，但重载 Xray 失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{
			"message":           fmt.Sprintf("已批量删除 %d 个核心节点，移除 %d 个托管代理，自动解绑 %d 个 key。", deletedCount, deletedProxyCount, unboundCount),
			"deletedCount":      deletedCount,
			"deletedProxyCount": deletedProxyCount,
			"unboundCount":      unboundCount,
			"runtime":           manager.Snapshot(),
		})
	}
}

func UpdateCoreProfileStatus(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseCoreProfileID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		var req updateCoreProfileStatusRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		status := normalizeCoreProfileManualStatus(req.Status)
		if status == "" {
			return c.Status(400).JSON(fiber.Map{"error": "状态只能是 Enabled 或 Disabled"})
		}
		updated, err := mutateCoreProfile(id, func(profile *models.CoreProfile) error {
			profile.Status = status
			profile.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			code := 500
			if errors.Is(err, errCoreProfileNotFound) {
				code = 404
			}
			return c.Status(code).JSON(fiber.Map{"error": err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "状态已更新，但重载 Xray 失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{"message": "核心节点状态更新成功", "profile": newCoreProfileResponse(updated), "runtime": manager.Snapshot()})
	}
}

func TestCoreProfile(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseCoreProfileID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Xray 重载失败: " + err.Error()})
		}
		result, _, err := executeCoreProfileConnectivityTest(context.Background(), id)
		if err != nil {
			status := 500
			if errors.Is(err, errCoreProfileNotFound) {
				status = 404
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}
		result["runtime"] = manager.Snapshot()
		return c.JSON(result)
	}
}

func BatchTestCoreProfiles(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req batchTestCoreProfilesRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		ids := normalizeCoreProfileIDs(req.IDs)
		if len(ids) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "请至少选择一个核心节点"})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Xray 重载失败: " + err.Error()})
		}
		items := make([]batchTestCoreProfilesItem, 0, len(ids))
		successCount := 0
		failedCount := 0
		target := ""
		for _, id := range ids {
			result, profileResp, err := executeCoreProfileConnectivityTest(context.Background(), id)
			if err != nil {
				failedCount++
				items = append(items, batchTestCoreProfilesItem{
					ID:      id,
					Name:    fmt.Sprintf("节点 #%d", id),
					Success: false,
					Message: err.Error(),
				})
				continue
			}
			item := batchTestCoreProfilesItem{
				ID:      profileResp.ID,
				Name:    profileResp.Name,
				Success: resultBoolValue(result, "success"),
				Message: resultStringValue(result, "message"),
				Target:  resultStringValue(result, "target"),
				Profile: &profileResp,
			}
			if item.Profile != nil && item.Profile.LastTest != nil {
				item.Summary = item.Profile.LastTest.Summary
			}
			if statusCode, ok := resultIntValue(result, "status_code"); ok {
				item.StatusCode = statusCode
			}
			if responseTime, ok := resultIntValue(result, "response_time"); ok {
				item.ResponseTime = responseTime
			}
			if target == "" && item.Target != "" {
				target = item.Target
			}
			if item.Success {
				successCount++
			} else {
				failedCount++
			}
			items = append(items, item)
		}
		message := fmt.Sprintf("批量测试完成：成功 %d 个，失败 %d 个。", successCount, failedCount)
		if target != "" {
			message += " 目标：" + target
		}
		return c.JSON(batchTestCoreProfilesResponse{
			Message:      message,
			Total:        len(ids),
			SuccessCount: successCount,
			FailedCount:  failedCount,
			Target:       target,
			Results:      items,
			Runtime:      manager.Snapshot(),
		})
	}
}

func executeCoreProfileConnectivityTest(ctx context.Context, id uint) (map[string]any, coreProfileResponse, error) {
	profile, err := getCoreProfileByID(id)
	if err != nil {
		return nil, coreProfileResponse{}, err
	}
	proxyCfg := models.NormalizeUpstreamProxy(models.UpstreamProxy{
		Name: profile.Name,
		Type: "socks5h",
		Host: models.CoreLocalHost,
		Port: profile.LocalPort,
	})
	result := testUpstreamProxyConnectivity(ctx, proxyCfg)
	record := buildProxyTestRecordFromResult(result)
	updated, updateErr := mutateCoreProfile(id, func(item *models.CoreProfile) error {
		item.LastTest = &record
		item.UpdatedAt = time.Now()
		return nil
	})
	current := profile
	if updateErr == nil {
		current = updated
	}
	response := newCoreProfileResponse(current)
	result["profile"] = response
	return result, response, nil
}

func deleteCoreProfilesByIDs(ids []uint) (int, int, int, error) {
	idSet := make(map[uint]struct{}, len(ids))
	for _, id := range normalizeCoreProfileIDs(ids) {
		idSet[id] = struct{}{}
	}
	if len(idSet) == 0 {
		return 0, 0, 0, errCoreProfileNotFound
	}
	deletedCount := 0
	deletedProxyCount := 0
	unboundCount := 0
	now := time.Now()
	err := db.UpdateStore(func(store *db.Store) error {
		removedManagedProxyIDs := make(map[uint]struct{}, len(idSet))
		keptProfiles := make([]models.CoreProfile, 0, len(store.CoreProfiles))
		for _, item := range store.CoreProfiles {
			if _, ok := idSet[item.ID]; !ok {
				keptProfiles = append(keptProfiles, item)
				continue
			}
			deletedCount++
			if item.ManagedProxyID > 0 {
				removedManagedProxyIDs[item.ManagedProxyID] = struct{}{}
			}
		}
		if deletedCount == 0 {
			return errCoreProfileNotFound
		}
		store.CoreProfiles = keptProfiles

		keptProxies := make([]models.UpstreamProxy, 0, len(store.Proxies))
		for _, proxy := range store.Proxies {
			if _, ok := removedManagedProxyIDs[proxy.ID]; ok {
				deletedProxyCount++
				continue
			}
			keptProxies = append(keptProxies, proxy)
		}
		store.Proxies = keptProxies

		for i := range store.APIKeys {
			if _, ok := removedManagedProxyIDs[store.APIKeys[i].ProxyID]; ok {
				store.APIKeys[i].ProxyID = 0
				store.APIKeys[i].UpdatedAt = now
				unboundCount++
			}
		}
		return nil
	})
	return deletedCount, deletedProxyCount, unboundCount, err
}

func normalizeCoreProfileIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	items := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, id)
	}
	return items
}

func resultStringValue(result map[string]any, key string) string {
	value, ok := result[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func resultBoolValue(result map[string]any, key string) bool {
	value, ok := result[key]
	if !ok || value == nil {
		return false
	}
	boolValue, ok := value.(bool)
	if ok {
		return boolValue
	}
	return false
}

func resultIntValue(result map[string]any, key string) (int, bool) {
	value, ok := result[key]
	if !ok || value == nil {
		return 0, false
	}
	switch item := value.(type) {
	case int:
		return item, true
	case int8:
		return int(item), true
	case int16:
		return int(item), true
	case int32:
		return int(item), true
	case int64:
		return int(item), true
	case float32:
		return int(item), true
	case float64:
		return int(item), true
	default:
		return 0, false
	}
}

func GetCoreRuntime(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(manager.Snapshot())
	}
}

func ReloadCoreRuntime(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "重载 Xray 失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{"message": "Xray 已重载", "runtime": manager.Snapshot()})
	}
}

func buildCoreProfileFromCreate(req createCoreProfileRequest) (models.CoreProfile, error) {
	authID, err := encryptCoreSecret(req.AuthID)
	if err != nil {
		return models.CoreProfile{}, err
	}
	password, err := encryptCoreSecret(req.Password)
	if err != nil {
		return models.CoreProfile{}, err
	}
	profile := models.NormalizeCoreProfile(models.CoreProfile{
		Name:             req.Name,
		Protocol:         req.Protocol,
		Status:           req.Status,
		Server:           req.Server,
		Port:             req.Port,
		LocalPort:        req.LocalPort,
		Transport:        req.Transport,
		TLSMode:          req.TLSMode,
		SNI:              req.SNI,
		AllowInsecure:    req.AllowInsecure,
		Host:             req.Host,
		Path:             req.Path,
		ServiceName:      req.ServiceName,
		Flow:             req.Flow,
		Method:           req.Method,
		Username:         req.Username,
		Password:         password,
		AuthID:           authID,
		Fingerprint:      req.Fingerprint,
		RealityPublicKey: req.RealityPublicKey,
		RealityShortID:   req.RealityShortID,
		RealitySpiderX:   req.RealitySpiderX,
		Remarks:          req.Remarks,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	})
	if err := validateCoreProfile(profile, req.AuthID, req.Password); err != nil {
		return models.CoreProfile{}, err
	}
	return profile, nil
}

func applyCoreProfileUpdate(profile *models.CoreProfile, req updateCoreProfileRequest) error {
	if profile == nil {
		return errCoreProfileNotFound
	}
	working := *profile
	if req.Name != nil {
		working.Name = *req.Name
	}
	if req.Protocol != nil {
		working.Protocol = *req.Protocol
	}
	if req.Status != nil {
		working.Status = *req.Status
	}
	if req.Server != nil {
		working.Server = *req.Server
	}
	if req.Port != nil {
		working.Port = *req.Port
	}
	if req.LocalPort != nil {
		working.LocalPort = *req.LocalPort
	}
	if req.Transport != nil {
		working.Transport = *req.Transport
	}
	if req.TLSMode != nil {
		working.TLSMode = *req.TLSMode
	}
	if req.SNI != nil {
		working.SNI = *req.SNI
	}
	if req.AllowInsecure != nil {
		working.AllowInsecure = *req.AllowInsecure
	}
	if req.Host != nil {
		working.Host = *req.Host
	}
	if req.Path != nil {
		working.Path = *req.Path
	}
	if req.ServiceName != nil {
		working.ServiceName = *req.ServiceName
	}
	if req.Flow != nil {
		working.Flow = *req.Flow
	}
	if req.Method != nil {
		working.Method = *req.Method
	}
	if req.Username != nil {
		working.Username = *req.Username
	}
	if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
		password, err := encryptCoreSecret(*req.Password)
		if err != nil {
			return err
		}
		working.Password = password
	}
	if req.AuthID != nil && strings.TrimSpace(*req.AuthID) != "" {
		authID, err := encryptCoreSecret(*req.AuthID)
		if err != nil {
			return err
		}
		working.AuthID = authID
	}
	if req.Fingerprint != nil {
		working.Fingerprint = *req.Fingerprint
	}
	if req.RealityPublicKey != nil {
		working.RealityPublicKey = *req.RealityPublicKey
	}
	if req.RealityShortID != nil {
		working.RealityShortID = *req.RealityShortID
	}
	if req.RealitySpiderX != nil {
		working.RealitySpiderX = *req.RealitySpiderX
	}
	if req.Remarks != nil {
		working.Remarks = *req.Remarks
	}
	working.UpdatedAt = time.Now()
	working = models.NormalizeCoreProfile(working)
	plainAuthID, _ := decryptCoreSecret(working.AuthID)
	plainPassword, _ := decryptCoreSecret(working.Password)
	if err := validateCoreProfile(working, plainAuthID, plainPassword); err != nil {
		return err
	}
	*profile = working
	return nil
}

func validateCoreProfile(profile models.CoreProfile, plainAuthID, plainPassword string) error {
	profile = models.NormalizeCoreProfile(profile)
	if profile.Name == "" {
		return errors.New("节点名称不能为空")
	}
	if profile.Server == "" {
		return errors.New("服务器地址不能为空")
	}
	if profile.Port <= 0 || profile.Port > 65535 {
		return errors.New("服务器端口必须在 1-65535 之间")
	}
	if profile.LocalPort < 0 || profile.LocalPort > 65535 {
		return errors.New("本地端口必须在 0-65535 之间")
	}
	switch profile.Protocol {
	case "vless", "vmess":
		if strings.TrimSpace(plainAuthID) == "" {
			return errors.New("该协议必须填写 UUID / Auth ID")
		}
	case "trojan":
		if strings.TrimSpace(plainPassword) == "" {
			return errors.New("Trojan 必须填写密码")
		}
	case "shadowsocks":
		if profile.Method == "" {
			return errors.New("Shadowsocks 必须填写加密方法")
		}
		if !models.IsSupportedShadowsocksMethod(profile.Method) {
			return fmt.Errorf("Shadowsocks 加密方法 %q 不受当前 Xray 支持；请改用以下方法之一：%s", profile.Method, strings.Join(models.SupportedShadowsocksMethods(), ", "))
		}
		if strings.TrimSpace(plainPassword) == "" {
			return errors.New("Shadowsocks 必须填写密码")
		}
	case "socks", "http":
		// optional auth
	default:
		return fmt.Errorf("不支持的协议: %s", profile.Protocol)
	}
	if profile.Transport != "tcp" && profile.Transport != "ws" && profile.Transport != "grpc" {
		return fmt.Errorf("不支持的传输协议: %s", profile.Transport)
	}
	if profile.TLSMode != "none" && profile.TLSMode != "tls" && profile.TLSMode != "reality" {
		return fmt.Errorf("不支持的 TLS 模式: %s", profile.TLSMode)
	}
	if profile.TLSMode == "reality" {
		if profile.RealityPublicKey == "" {
			return errors.New("Reality 模式必须填写 public key")
		}
		if profile.SNI == "" {
			return errors.New("Reality 模式必须填写 SNI")
		}
	}
	return nil
}

func mutateCoreProfile(id uint, mutator func(*models.CoreProfile) error) (models.CoreProfile, error) {
	var updated models.CoreProfile
	err := db.UpdateStore(func(store *db.Store) error {
		for i := range store.CoreProfiles {
			if store.CoreProfiles[i].ID != id {
				continue
			}
			if err := mutator(&store.CoreProfiles[i]); err != nil {
				return err
			}
			updated = store.CoreProfiles[i]
			return nil
		}
		return errCoreProfileNotFound
	})
	return updated, err
}

func getCoreProfileByID(id uint) (models.CoreProfile, error) {
	store, err := db.ReadStore()
	if err != nil {
		return models.CoreProfile{}, err
	}
	for _, item := range store.CoreProfiles {
		if item.ID == id {
			return item, nil
		}
	}
	return models.CoreProfile{}, errCoreProfileNotFound
}

func parseCoreProfileID(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("无效的核心节点 ID")
	}
	return uint(id), nil
}

func normalizeCoreProfileManualStatus(status string) string {
	switch strings.TrimSpace(status) {
	case models.CoreProfileStatusEnabled:
		return models.CoreProfileStatusEnabled
	case models.CoreProfileStatusDisabled:
		return models.CoreProfileStatusDisabled
	default:
		return ""
	}
}

func newCoreProfileResponse(profile models.CoreProfile) coreProfileResponse {
	profile = models.NormalizeCoreProfile(profile)
	return coreProfileResponse{
		ID:               profile.ID,
		Name:             profile.Name,
		Protocol:         profile.Protocol,
		Status:           profile.Status,
		Server:           profile.Server,
		Port:             profile.Port,
		LocalPort:        profile.LocalPort,
		LocalProxyURL:    fmt.Sprintf("socks5h://%s:%d", models.CoreLocalHost, profile.LocalPort),
		ManagedProxyID:   profile.ManagedProxyID,
		Transport:        profile.Transport,
		TLSMode:          profile.TLSMode,
		SNI:              profile.SNI,
		AllowInsecure:    profile.AllowInsecure,
		Host:             profile.Host,
		Path:             profile.Path,
		ServiceName:      profile.ServiceName,
		Flow:             profile.Flow,
		Method:           profile.Method,
		Username:         profile.Username,
		HasPassword:      strings.TrimSpace(profile.Password) != "",
		HasAuthID:        strings.TrimSpace(profile.AuthID) != "",
		Fingerprint:      profile.Fingerprint,
		RealityPublicKey: profile.RealityPublicKey,
		RealityShortID:   profile.RealityShortID,
		RealitySpiderX:   profile.RealitySpiderX,
		Remarks:          profile.Remarks,
		LastTest:         buildProxyTestResponse(profile.LastTest),
		CreatedAt:        profile.CreatedAt,
		UpdatedAt:        profile.UpdatedAt,
	}
}
