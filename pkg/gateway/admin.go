package gateway

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"
	"nvidia-api-gateway/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

type apiKeyResponse struct {
	ID        uint      `json:"id"`
	Name      string    `json:"name"`
	Weight    float64   `json:"weight"`
	Status    string    `json:"status"`
	ProbeOnly bool      `json:"probeOnly"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type apiKeysResponse struct {
	Keys []apiKeyResponse `json:"keys"`
}

type createAPIKeyRequest struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Weight    float64 `json:"weight"`
	ProbeOnly bool    `json:"probeOnly"`
}

type updateAPIKeyRequest struct {
	Key       string   `json:"key"`
	Name      *string  `json:"name"`
	Weight    *float64 `json:"weight"`
	ProbeOnly *bool    `json:"probeOnly"`
}

type updateAPIKeyStatusRequest struct {
	Status string `json:"status"`
}

type systemConfigResponse struct {
	UpstreamBaseURL         string `json:"upstreamBaseURL"`
	SchedulerStrategy       string `json:"schedulerStrategy"`
	MaxRetries              int    `json:"maxRetries"`
	MaxConcurrency          int    `json:"maxConcurrency"`
	RequestTimeoutSecond    int    `json:"requestTimeoutSecond"`
	GatewayBaseURL          string `json:"gatewayBaseURL"`
	FirstByteTimeoutMs      int    `json:"firstByteTimeoutMs"`
	HealthProbeTimeoutSec   int    `json:"healthProbeTimeoutSecond"`
	StreamIdleTimeoutSec    int    `json:"streamIdleTimeoutSecond"`
	StreamKeepAliveSec      int    `json:"streamKeepAliveSecond"`
	TransportRetryCount     int    `json:"transportRetryCount"`
	TransportRetryBackoffMs int    `json:"transportRetryBackoffMs"`
	EnableOpenAI            bool   `json:"enableOpenAI"`
	EnableClaude            bool   `json:"enableClaude"`
	EnableGemini            bool   `json:"enableGemini"`
	AnonymousAccess         bool   `json:"anonymousAccess"`
}

type updateSystemConfigRequest struct {
	UpstreamBaseURL         *string `json:"upstreamBaseURL"`
	SchedulerStrategy       *string `json:"schedulerStrategy"`
	MaxRetries              *int    `json:"maxRetries"`
	MaxConcurrency          *int    `json:"maxConcurrency"`
	RequestTimeoutSecond    *int    `json:"requestTimeoutSecond"`
	FirstByteTimeoutMs      *int    `json:"firstByteTimeoutMs"`
	HealthProbeTimeoutSec   *int    `json:"healthProbeTimeoutSecond"`
	StreamIdleTimeoutSec    *int    `json:"streamIdleTimeoutSecond"`
	StreamKeepAliveSec      *int    `json:"streamKeepAliveSecond"`
	TransportRetryCount     *int    `json:"transportRetryCount"`
	TransportRetryBackoffMs *int    `json:"transportRetryBackoffMs"`
	EnableOpenAI            *bool   `json:"enableOpenAI"`
	EnableClaude            *bool   `json:"enableClaude"`
	EnableGemini            *bool   `json:"enableGemini"`
	AnonymousAccess         *bool   `json:"anonymousAccess"`
}

func AddAPIKey(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req createAPIKeyRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "\u8bf7\u6c42\u4f53\u683c\u5f0f\u65e0\u6548"})
		}

		rootKey := utils.GetEncryptionKey()
		if rootKey == "" || len(rootKey) != 32 {
			return c.Status(500).JSON(fiber.Map{"error": "\u670d\u52a1\u7aef\u7f3a\u5c11\u5408\u6cd5\u7684 32 \u4f4d ENCRYPTION_KEY"})
		}

		name, weight, err := validateAPIKeyCreate(req)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		encryptedKey, err := utils.Encrypt(strings.TrimSpace(req.Key), rootKey)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u52a0\u5bc6\u4e0a\u6e38 Key \u5931\u8d25"})
		}

		now := time.Now()
		apiKey := models.APIKey{
			Key:       encryptedKey,
			Name:      name,
			Weight:    weight,
			Status:    APIKeyStatusActive,
			ProbeOnly: req.ProbeOnly,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := db.UpdateStore(func(store *db.Store) error {
			apiKey.ID = store.NextAPIID
			store.NextAPIID++
			store.APIKeys = append(store.APIKeys, apiKey)
			return nil
		}); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u4e0a\u6e38 Key \u5df2\u4fdd\u5b58\uff0c\u4f46\u91cd\u8f7d\u8c03\u5ea6\u5668\u5931\u8d25"})
		}

		return c.JSON(fiber.Map{
			"message": "\u4e0a\u6e38 Key \u6dfb\u52a0\u6210\u529f",
			"key":     newAPIKeyResponse(apiKey),
		})
	}
}

func GetAPIKeys(c *fiber.Ctx) error {
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u4e0a\u6e38 Key \u5931\u8d25"})
	}
	return c.JSON(apiKeysResponse{Keys: buildAPIKeyResponses(store.APIKeys)})
}

func UpdateAPIKey(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseAPIKeyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		var req updateAPIKeyRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "\u8bf7\u6c42\u4f53\u683c\u5f0f\u65e0\u6548"})
		}

		rootKey := utils.GetEncryptionKey()
		if rootKey == "" || len(rootKey) != 32 {
			return c.Status(500).JSON(fiber.Map{"error": "\u670d\u52a1\u7aef\u7f3a\u5c11\u5408\u6cd5\u7684 32 \u4f4d ENCRYPTION_KEY"})
		}

		updatedKey, err := mutateAPIKeyWithStore(id, func(store *db.Store, key *models.APIKey) error {
			if req.Name != nil {
				name := strings.TrimSpace(*req.Name)
				if name == "" {
					return errors.New("\u540d\u79f0\u4e0d\u80fd\u4e3a\u7a7a")
				}
				key.Name = name
			}
			if req.Weight != nil {
				if *req.Weight <= 0 {
					return errors.New("\u6743\u91cd\u5fc5\u987b\u5927\u4e8e 0")
				}
				key.Weight = *req.Weight
			}
			if strings.TrimSpace(req.Key) != "" {
				encryptedKey, encryptErr := utils.Encrypt(strings.TrimSpace(req.Key), rootKey)
				if encryptErr != nil {
					return errors.New("\u52a0\u5bc6\u4e0a\u6e38 Key \u5931\u8d25")
				}
				key.Key = encryptedKey
			}
			if req.ProbeOnly != nil {
				key.ProbeOnly = *req.ProbeOnly
			}
			key.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			status := 500
			if errors.Is(err, errAPIKeyNotFound) {
				status = 404
			} else if err.Error() == "\u540d\u79f0\u4e0d\u80fd\u4e3a\u7a7a" || err.Error() == "\u6743\u91cd\u5fc5\u987b\u5927\u4e8e 0" {
				status = 400
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u4e0a\u6e38 Key \u5df2\u66f4\u65b0\uff0c\u4f46\u91cd\u8f7d\u8c03\u5ea6\u5668\u5931\u8d25"})
		}

		return c.JSON(fiber.Map{
			"message": "\u4e0a\u6e38 Key \u66f4\u65b0\u6210\u529f",
			"key":     newAPIKeyResponse(updatedKey),
		})
	}
}

// RevealAPIKeyPlaintext 返回单个上游 Key 的明文（解密存储值）。
// 供管理后台“复制”按钮使用；仅管理端可访问。
func RevealAPIKeyPlaintext(c *fiber.Ctx) error {
	id, err := parseAPIKeyID(c)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	rootKey := utils.GetEncryptionKey()
	if rootKey == "" || len(rootKey) != 32 {
		return c.Status(500).JSON(fiber.Map{"error": "\u670d\u52a1\u7aef\u7f3a\u5c11\u5408\u6cd5\u7684 32 \u4f4d ENCRYPTION_KEY"})
	}

	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u4e0a\u6e38 Key \u5931\u8d25"})
	}
	for _, key := range store.APIKeys {
		if key.ID != id {
			continue
		}
		plaintext, decryptErr := utils.Decrypt(key.Key, rootKey)
		if decryptErr != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u8be5 Key \u65e0\u6cd5\u89e3\u5bc6"})
		}
		return c.JSON(fiber.Map{
			"id":        key.ID,
			"name":      key.Name,
			"plainKey":  plaintext,
			"plaintext": plaintext,
		})
	}
	return c.Status(404).JSON(fiber.Map{"error": errAPIKeyNotFound.Error()})
}

// ExportAPIKeys 批量导出上游 Key，格式与批量导入兼容：
// 每行 `name,key,weight`（或仅 `key`），分隔符统一用逗号，逐行一个 Key。
// 返回纯文本，可直接复制回“批量导入”粘贴框，或另存为 txt 文件。
func ExportAPIKeys(c *fiber.Ctx) error {
	rootKey := utils.GetEncryptionKey()
	if rootKey == "" || len(rootKey) != 32 {
		return c.Status(500).JSON(fiber.Map{"error": "\u670d\u52a1\u7aef\u7f3a\u5c11\u5408\u6cd5\u7684 32 \u4f4d ENCRYPTION_KEY"})
	}

	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u4e0a\u6e38 Key \u5931\u8d25"})
	}

	var builder strings.Builder
	for _, key := range store.APIKeys {
		plaintext, decryptErr := utils.Decrypt(key.Key, rootKey)
		if decryptErr != nil {
			continue
		}
		// 每行仅输出 Key 明文，方便直接粘贴使用或再次导入。
		builder.WriteString(plaintext)
		builder.WriteString("\n")
	}

	c.Set("Content-Type", "text/plain; charset=utf-8")
	c.Set("Content-Disposition", "attachment; filename=api_keys_export.txt")
	return c.SendString(builder.String())
}

func DeleteAPIKey(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseAPIKeyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		if err := deleteAPIKey(id); err != nil {
			status := 500
			if errors.Is(err, errAPIKeyNotFound) {
				status = 404
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u4e0a\u6e38 Key \u5df2\u5220\u9664\uff0c\u4f46\u91cd\u8f7d\u8c03\u5ea6\u5668\u5931\u8d25"})
		}

		return c.JSON(fiber.Map{"message": "\u4e0a\u6e38 Key \u5220\u9664\u6210\u529f"})
	}
}

func UpdateAPIKeyStatus(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseAPIKeyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		var req updateAPIKeyStatusRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "\u8bf7\u6c42\u4f53\u683c\u5f0f\u65e0\u6548"})
		}

		status, err := normalizeManualStatus(req.Status)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		updatedKey, err := mutateAPIKey(id, func(key *models.APIKey) error {
			key.Status = status
			key.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			code := 500
			if errors.Is(err, errAPIKeyNotFound) {
				code = 404
			}
			return c.Status(code).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u72b6\u6001\u5df2\u66f4\u65b0\uff0c\u4f46\u91cd\u8f7d\u8c03\u5ea6\u5668\u5931\u8d25"})
		}

		return c.JSON(fiber.Map{
			"message": "\u4e0a\u6e38 Key \u72b6\u6001\u66f4\u65b0\u6210\u529f",
			"key":     newAPIKeyResponse(updatedKey),
		})
	}
}

func ProbeAPIKey(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := parseAPIKeyID(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		rootKey := utils.GetEncryptionKey()
		if rootKey == "" || len(rootKey) != 32 {
			return c.Status(500).JSON(fiber.Map{"error": "\u670d\u52a1\u7aef\u7f3a\u5c11\u5408\u6cd5\u7684 32 \u4f4d ENCRYPTION_KEY"})
		}

		store, err := db.ReadStore()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u4e0a\u6e38 Key \u5931\u8d25"})
		}

		var current *models.APIKey
		for i := range store.APIKeys {
			if store.APIKeys[i].ID == id {
				copy := store.APIKeys[i]
				current = &copy
				break
			}
		}
		if current == nil {
			return c.Status(404).JSON(fiber.Map{"error": "\u4e0a\u6e38 Key \u4e0d\u5b58\u5728"})
		}

		plaintext, err := utils.Decrypt(current.Key, rootKey)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u89e3\u5bc6\u4e0a\u6e38 Key \u5931\u8d25"})
		}

		probe, ok := probeKeyStatus(context.Background(), plaintext)
		if !ok || probe == nil {
			return c.Status(502).JSON(fiber.Map{"error": "\u63a2\u6d4b\u4e0a\u6e38 Key \u72b6\u6001\u5931\u8d25"})
		}

		updatedKey, err := mutateAPIKey(id, func(key *models.APIKey) error {
			key.Status = probe.Status
			key.UpdatedAt = time.Now()
			return nil
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u4e0a\u6e38 Key \u63a2\u6d4b\u6210\u529f\uff0c\u4f46\u91cd\u8f7d\u8c03\u5ea6\u5668\u5931\u8d25"})
		}

		return c.JSON(fiber.Map{
			"message": "\u4e0a\u6e38 Key \u63a2\u6d4b\u5b8c\u6210",
			"key":     newAPIKeyResponse(updatedKey),
			"probe": fiber.Map{
				"endpoint":   probe.Endpoint,
				"method":     probe.Method,
				"httpStatus": probe.HTTPStatus,
				"durationMs": probe.DurationMs,
				"detail":     probe.Detail,
			},
		})
	}
}

func ReloadSystem(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := context.Background()
		if err := LoadActiveKeys(ctx, sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		stats, err := sched.Stats(ctx)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		return c.JSON(fiber.Map{
			"message": "\u8c03\u5ea6\u5668\u70ed\u91cd\u8f7d\u5b8c\u6210",
			"stats":   stats,
		})
	}
}

func SchedulerStats(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		store, err := db.ReadStore()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u8c03\u5ea6\u7edf\u8ba1\u5931\u8d25"})
		}

		stats, err := sched.Stats(context.Background())
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if stats.Active == 0 {
			for _, key := range store.APIKeys {
				switch key.Status {
				case APIKeyStatusActive:
					stats.Active++
				case APIKeyStatusCooling:
					stats.Cooling++
				case APIKeyStatusDead, APIKeyStatusDisabled:
					stats.Dead++
				}
			}
		}
		return c.JSON(stats)
	}
}

func GetSystemConfig(c *fiber.Ctx) error {
	store, err := db.ReadStore()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "\u8bfb\u53d6\u7cfb\u7edf\u914d\u7f6e\u5931\u8d25"})
	}
	return c.JSON(newSystemConfigResponse(resolveStoredSystemConfig(store)))
}

func UpdateSystemConfig(sched *scheduler.Scheduler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req updateSystemConfigRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
		}

		var updatedConfig models.SystemConfig
		err := db.UpdateStore(func(store *db.Store) error {
			cfg := store.SystemConfig
			if req.UpstreamBaseURL != nil {
				cfg.UpstreamBaseURL = strings.TrimSpace(*req.UpstreamBaseURL)
			}
			if req.SchedulerStrategy != nil {
				cfg.SchedulerStrategy = strings.TrimSpace(*req.SchedulerStrategy)
			}
			if req.MaxRetries != nil {
				cfg.MaxRetries = *req.MaxRetries
			}
			if req.MaxConcurrency != nil {
				cfg.MaxConcurrency = *req.MaxConcurrency
			}
			if req.RequestTimeoutSecond != nil {
				cfg.RequestTimeoutSecond = *req.RequestTimeoutSecond
			}
			if req.FirstByteTimeoutMs != nil {
				cfg.FirstByteTimeoutMs = *req.FirstByteTimeoutMs
			}
			if req.HealthProbeTimeoutSec != nil {
				cfg.HealthProbeTimeoutSec = *req.HealthProbeTimeoutSec
			}
			if req.StreamIdleTimeoutSec != nil {
				cfg.StreamIdleTimeoutSec = *req.StreamIdleTimeoutSec
			}
			if req.StreamKeepAliveSec != nil {
				cfg.StreamKeepAliveSec = *req.StreamKeepAliveSec
			}
			if req.TransportRetryCount != nil {
				cfg.TransportRetryCount = *req.TransportRetryCount
			}
			if req.TransportRetryBackoffMs != nil {
				cfg.TransportRetryBackoffMs = *req.TransportRetryBackoffMs
			}
			if req.EnableOpenAI != nil {
				cfg.EnableOpenAI = *req.EnableOpenAI
			}
			if req.EnableClaude != nil {
				cfg.EnableClaude = *req.EnableClaude
			}
			if req.EnableGemini != nil {
				cfg.EnableGemini = *req.EnableGemini
			}
			if req.AnonymousAccess != nil {
				cfg.AnonymousAccess = *req.AnonymousAccess
			}
			cfg = models.NormalizeSystemConfig(cfg)
			store.SystemConfig = cfg
			updatedConfig = resolveStoredSystemConfig(store)
			return nil
		})
		if err != nil {
			status := 500
			if err.Error() == "???????" {
				status = 400
			}
			return c.Status(status).JSON(fiber.Map{"error": err.Error()})
		}

		if err := LoadActiveKeys(context.Background(), sched); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "system config saved, but scheduler reload failed"})
		}

		return c.JSON(fiber.Map{
			"message": "system config updated successfully",
			"config":  newSystemConfigResponse(updatedConfig),
		})
	}
}

func newSystemConfigResponse(cfg models.SystemConfig) systemConfigResponse {
	cfg = models.NormalizeSystemConfig(cfg)
	return systemConfigResponse{
		UpstreamBaseURL:         cfg.UpstreamBaseURL,
		SchedulerStrategy:       cfg.SchedulerStrategy,
		MaxRetries:              cfg.MaxRetries,
		MaxConcurrency:          cfg.MaxConcurrency,
		RequestTimeoutSecond:    cfg.RequestTimeoutSecond,
		GatewayBaseURL:          gatewayBaseURL(),
		FirstByteTimeoutMs:      cfg.FirstByteTimeoutMs,
		HealthProbeTimeoutSec:   cfg.HealthProbeTimeoutSec,
		StreamIdleTimeoutSec:    cfg.StreamIdleTimeoutSec,
		StreamKeepAliveSec:      cfg.StreamKeepAliveSec,
		TransportRetryCount:     cfg.TransportRetryCount,
		TransportRetryBackoffMs: cfg.TransportRetryBackoffMs,
		EnableOpenAI:            cfg.EnableOpenAI,
		EnableClaude:            cfg.EnableClaude,
		EnableGemini:            cfg.EnableGemini,
		AnonymousAccess:         cfg.AnonymousAccess,
	}
}

var errAPIKeyNotFound = errors.New("\u4e0a\u6e38 Key \u4e0d\u5b58\u5728")

func validateAPIKeyCreate(req createAPIKeyRequest) (string, float64, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", 0, errors.New("\u540d\u79f0\u4e0d\u80fd\u4e3a\u7a7a")
	}
	if strings.TrimSpace(req.Key) == "" {
		return "", 0, errors.New("Key \u4e0d\u80fd\u4e3a\u7a7a")
	}
	if req.Weight <= 0 {
		return "", 0, errors.New("\u6743\u91cd\u5fc5\u987b\u5927\u4e8e 0")
	}
	return name, req.Weight, nil
}

func normalizeManualStatus(status string) (string, error) {
	switch strings.TrimSpace(status) {
	case APIKeyStatusActive:
		return APIKeyStatusActive, nil
	case APIKeyStatusDisabled:
		return APIKeyStatusDisabled, nil
	default:
		return "", errors.New("\u72b6\u6001\u53ea\u80fd\u662f Active \u6216 Disabled")
	}
}

func parseAPIKeyID(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("\u65e0\u6548\u7684\u4e0a\u6e38 Key ID")
	}
	return uint(id), nil
}

func mutateAPIKey(id uint, mutator func(*models.APIKey) error) (models.APIKey, error) {
	return mutateAPIKeyWithStore(id, func(_ *db.Store, key *models.APIKey) error {
		return mutator(key)
	})
}

func mutateAPIKeyWithStore(id uint, mutator func(*db.Store, *models.APIKey) error) (models.APIKey, error) {
	var updated models.APIKey
	err := db.UpdateStore(func(store *db.Store) error {
		for i := range store.APIKeys {
			if store.APIKeys[i].ID != id {
				continue
			}
			if err := mutator(store, &store.APIKeys[i]); err != nil {
				return err
			}
			updated = store.APIKeys[i]
			return nil
		}
		return errAPIKeyNotFound
	})
	return updated, err
}

func deleteAPIKey(id uint) error {
	return db.UpdateStore(func(store *db.Store) error {
		for i := range store.APIKeys {
			if store.APIKeys[i].ID != id {
				continue
			}
			store.APIKeys = append(store.APIKeys[:i], store.APIKeys[i+1:]...)
			return nil
		}
		return errAPIKeyNotFound
	})
}

func buildAPIKeyResponses(keys []models.APIKey) []apiKeyResponse {
	items := make([]apiKeyResponse, 0, len(keys))
	for _, key := range keys {
		items = append(items, newAPIKeyResponse(key))
	}
	return items
}

func newAPIKeyResponse(key models.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:        key.ID,
		Name:      key.Name,
		Weight:    key.Weight,
		Status:    key.Status,
		ProbeOnly: key.ProbeOnly,
		CreatedAt: key.CreatedAt,
		UpdatedAt: key.UpdatedAt,
	}
}

// GatewayVersion 由构建流程注入，可在编译时通过 -ldflags 覆盖：
//   go build -ldflags "-X nvidia-api-gateway/pkg/gateway.GatewayVersion=v1.2.3" ./main.go
var GatewayVersion = "v3.4.0"

// AdminVersion 供管理后台显示后端版本号。
// 该路由挂在 /admin 组内，已由 AdminAuthMiddleware 校验 X-Admin-Token。
func AdminVersion() fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"version":   GatewayVersion,
			"service":   "nvidia-api-gateway",
			"proxyMode": "system-default",
		})
	}
}

// AdminAuthCheck 供前端登录页验证管理后台密码是否正确。
// 该路由挂在 /admin 组内，已由 AdminAuthMiddleware 校验 X-Admin-Token。
// 到达这里即代表凭证有效。
func AdminAuthCheck() fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	}
}
