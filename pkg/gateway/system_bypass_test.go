package gateway

import (
	"strings"
	"testing"

	"nvidia-api-gateway/pkg/models"
)

func TestBuildUpstreamURLRoutesBypassedModelsToOfficial(t *testing.T) {
	proxyBase := "http://161.118.226.201:2260/zqt1125/ywd/https/integrate.api.nvidia.com/v1"
	cfg := models.NormalizeSystemConfig(models.SystemConfig{
		UpstreamBaseURL:   proxyBase,
		BypassProxyModels: []string{"google/diffusiongemma-26b-a4b-it"},
	})

	proxied := buildUpstreamURL(cfg, "chat/completions", "meta/llama-3.1-8b-instruct")
	if !strings.Contains(proxied, "161.118.226.201:2260") {
		t.Fatalf("未命中放行列表的模型应走配置的上游地址，实际得到 %s", proxied)
	}

	bypassed := buildUpstreamURL(cfg, "chat/completions", "google/diffusiongemma-26b-a4b-it")
	if !strings.HasPrefix(bypassed, models.DefaultUpstreamBaseURL) {
		t.Fatalf("放行模型应直连官方地址，实际得到 %s", bypassed)
	}
	if strings.Contains(bypassed, "161.118.226.201") {
		t.Fatalf("放行模型不应经过上游代理，实际得到 %s", bypassed)
	}

	upper := buildUpstreamURL(cfg, "chat/completions", "Google/DiffusionGemma-26B-A4B-IT")
	if !strings.HasPrefix(upper, models.DefaultUpstreamBaseURL) {
		t.Fatalf("放行匹配应忽略大小写，实际得到 %s", upper)
	}
}

func TestBypassProxyModelsNormalized(t *testing.T) {
	cfg := models.NormalizeSystemConfig(models.SystemConfig{
		BypassProxyModels: []string{"  google/a  ", "", "google/a", "meta/b", "   "},
	})
	if len(cfg.BypassProxyModels) != 2 {
		t.Fatalf("放行列表应去空白、去空项、去重后剩 2 项，实际 %d 项: %#v", len(cfg.BypassProxyModels), cfg.BypassProxyModels)
	}
	if cfg.BypassProxyModels[0] != "google/a" || cfg.BypassProxyModels[1] != "meta/b" {
		t.Fatalf("放行列表应保持首次出现顺序，实际 %#v", cfg.BypassProxyModels)
	}
}
