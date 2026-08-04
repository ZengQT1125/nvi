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
	cfg := models.NormalizeSystemConfig(store.SystemConfig)
	if cfg.UpstreamProxyID == 0 {
		return cfg
	}
	for _, proxy := range store.Proxies {
		if proxy.ID != cfg.UpstreamProxyID {
			continue
		}
		proxyURL, err := buildProxyURLFromModel(proxy)
		if err == nil {
			cfg.UpstreamProxyURL = proxyURL
		}
		return cfg
	}
	cfg.UpstreamProxyID = 0
	return cfg
}

func buildUpstreamURL(cfg models.SystemConfig, endpointPath string) string {
	base := strings.TrimSpace(cfg.UpstreamBaseURL)
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
