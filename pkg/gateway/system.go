package gateway

import (
	"net/url"
	"path"
	"strings"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"
)

func loadSystemConfig() models.SystemConfig {
	store, err := db.ReadStore()
	if err != nil {
		return models.DefaultSystemConfig()
	}
	return resolveStoredSystemConfig(store)
}

func resolveStoredSystemConfig(store *db.Store) models.SystemConfig {
	if store == nil {
		return models.DefaultSystemConfig()
	}
	return models.NormalizeSystemConfig(store.SystemConfig)
}

// buildUpstreamURL 拼接上游请求地址。
// model 命中「放行模型」时忽略配置里的 UpstreamBaseURL（通常是中转代理），
// 直接使用官方默认地址；其余模型仍走配置的地址。
func buildUpstreamURL(cfg models.SystemConfig, endpointPath, model string) string {
	base := strings.TrimSpace(cfg.UpstreamBaseURL)
	if modelIsBypassed(cfg, model) {
		base = models.DefaultUpstreamBaseURL
	}
	if base == "" {
		base = models.DefaultUpstreamBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return models.DefaultUpstreamBaseURL + endpointPath
	}
	u.Path = path.Join(u.Path, endpointPath)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}

// modelIsBypassed 判断模型是否在放行列表里（忽略大小写与首尾空白）。
func modelIsBypassed(cfg models.SystemConfig, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, candidate := range cfg.BypassProxyModels {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			return true
		}
	}
	return false
}

func protocolEnabled(cfg models.SystemConfig, protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return cfg.EnableOpenAI
	case "claude":
		return cfg.EnableClaude
	case "gemini":
		return cfg.EnableGemini
	default:
		return false
	}
}

func gatewayBaseURL() string {
	return utils.ResolvePublicGatewayBaseURL()
}
