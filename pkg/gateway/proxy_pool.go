package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"
)

type upstreamKeyRuntimeInfo struct {
	KeyID     uint
	KeyName   string
	ProxyID   uint
	ProxyName string
	ProxyURL  string
}

type upstreamProxyRuntimeInfo struct {
	ID        uint
	Name      string
	Type      string
	URL       string
	HostPort  string
	Username  string
	ManagedBy string
}

var (
	upstreamRuntimeMu             sync.RWMutex
	upstreamRuntimeByKey          = map[string]upstreamKeyRuntimeInfo{}
	upstreamRuntimeByProxy        = map[uint]upstreamProxyRuntimeInfo{}
	upstreamRuntimeFailoverByID   = map[uint]upstreamProxyRuntimeInfo{}
	upstreamRuntimeFailoverByURL  = map[string]upstreamProxyRuntimeInfo{}
	upstreamRuntimeCoolingByProxy = map[uint]time.Time{}
)

func normalizeProxyType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateProxyType(value string) error {
	switch normalizeProxyType(value) {
	case "http", "https", "socks5", "socks5h", "dokodemo":
		return nil
	default:
		return fmt.Errorf("unsupported proxy type: %s", strings.TrimSpace(value))
	}
}

func validateUpstreamProxyStatus(value string) error {
	switch strings.TrimSpace(value) {
	case models.ProxyStatusEnabled, models.ProxyStatusDisabled:
		return nil
	default:
		return fmt.Errorf("unsupported proxy status: %s", strings.TrimSpace(value))
	}
}

func validateUpstreamProxyModel(proxy models.UpstreamProxy) error {
	proxy = models.NormalizeUpstreamProxy(proxy)
	if proxy.Name == "" {
		return fmt.Errorf("代理名称不能为空")
	}
	if err := validateProxyType(proxy.Type); err != nil {
		return err
	}
	if proxy.Host == "" {
		return fmt.Errorf("代理主机不能为空")
	}
	if proxy.Port <= 0 || proxy.Port > 65535 {
		return fmt.Errorf("代理端口必须在 1-65535 之间")
	}
	return nil
}

func encryptProxyPassword(raw string) (string, error) {
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		return "", fmt.Errorf("missing ENCRYPTION_KEY")
	}
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return utils.Encrypt(strings.TrimSpace(raw), secret)
}

func decryptProxyPassword(raw string) (string, error) {
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		return "", fmt.Errorf("missing ENCRYPTION_KEY")
	}
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return utils.Decrypt(strings.TrimSpace(raw), secret)
}

func buildProxyURLFromModel(proxy models.UpstreamProxy) (string, error) {
	proxy = models.NormalizeUpstreamProxy(proxy)
	if err := validateUpstreamProxyModel(proxy); err != nil {
		return "", err
	}
	password, err := decryptProxyPassword(proxy.Password)
	if err != nil {
		return "", err
	}
	proxyURL := &url.URL{
		Scheme: proxy.Type,
		Host:   net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port)),
	}
	if proxy.Username != "" || password != "" {
		if password != "" {
			proxyURL.User = url.UserPassword(proxy.Username, password)
		} else {
			proxyURL.User = url.User(proxy.Username)
		}
	}
	return proxyURL.String(), nil
}

func buildProxyPreviewFromModel(proxy models.UpstreamProxy) string {
	proxy = models.NormalizeUpstreamProxy(proxy)
	userInfo := ""
	if proxy.Username != "" {
		userInfo = proxy.Username + "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", proxy.Type, userInfo, proxy.Host, proxy.Port)
}

func buildProxyRuntimeIndex(proxies []models.UpstreamProxy) map[uint]upstreamProxyRuntimeInfo {
	items := make(map[uint]upstreamProxyRuntimeInfo, len(proxies))
	for _, proxy := range proxies {
		proxy = models.NormalizeUpstreamProxy(proxy)
		if proxy.ID == 0 || !isProxyEnabled(proxy) {
			continue
		}
		url, err := buildProxyURLFromModel(proxy)
		if err != nil {
			continue
		}
		items[proxy.ID] = upstreamProxyRuntimeInfo{
			ID:        proxy.ID,
			Name:      proxy.Name,
			Type:      proxy.Type,
			URL:       url,
			HostPort:  fmt.Sprintf("%s:%d", proxy.Host, proxy.Port),
			Username:  proxy.Username,
			ManagedBy: proxy.ManagedBy,
		}
	}
	return items
}

func rebuildUpstreamRuntime(store *db.Store) error {
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		upstreamRuntimeMu.Lock()
		upstreamRuntimeByKey = map[string]upstreamKeyRuntimeInfo{}
		upstreamRuntimeByProxy = map[uint]upstreamProxyRuntimeInfo{}
		upstreamRuntimeMu.Unlock()
		return fmt.Errorf("missing ENCRYPTION_KEY")
	}
	proxyIndex := buildProxyRuntimeIndex(store.Proxies)
	keyIndex := make(map[string]upstreamKeyRuntimeInfo, len(store.APIKeys))
	for _, key := range store.APIKeys {
		plaintext, err := utils.Decrypt(key.Key, secret)
		if err != nil || strings.TrimSpace(plaintext) == "" {
			continue
		}
		info := upstreamKeyRuntimeInfo{
			KeyID:   key.ID,
			KeyName: key.Name,
			ProxyID: key.ProxyID,
		}
		if proxyInfo, ok := proxyIndex[key.ProxyID]; ok {
			info.ProxyName = proxyInfo.Name
			info.ProxyURL = proxyInfo.URL
		}
		keyIndex[plaintext] = info
	}
	upstreamRuntimeMu.Lock()
	upstreamRuntimeByKey = keyIndex
	upstreamRuntimeByProxy = proxyIndex
	upstreamRuntimeMu.Unlock()
	return nil
}

func proxyRuntimeURLKey(raw string) string {
	return strings.TrimSpace(raw)
}

func selectAlternateManagedProxyURLForAPIKey(cfg models.SystemConfig, plaintextKey string, failedProxyURL string) (string, bool) {
	failedProxyURL = strings.TrimSpace(failedProxyURL)
	if failedProxyURL == "" {
		return "", false
	}
	currentInfo, ok := effectiveProxyRuntimeInfoForAPIKey(cfg, plaintextKey)
	if !ok || strings.TrimSpace(currentInfo.ManagedBy) != models.CoreManagedByXray {
		return "", false
	}
	store, err := db.ReadStore()
	if err != nil || store == nil {
		return "", false
	}
	proxyIndex := buildProxyRuntimeIndex(store.Proxies)
	type candidate struct {
		info      upstreamProxyRuntimeInfo
		success   bool
		latencyMs int64
		id        uint
	}
	candidates := make([]candidate, 0)
	now := time.Now()
	for _, proxy := range store.Proxies {
		proxy = models.NormalizeUpstreamProxy(proxy)
		if proxy.ID == 0 || proxy.ID == currentInfo.ID || proxy.ManagedBy != models.CoreManagedByXray || !isProxyEnabled(proxy) {
			continue
		}
		if isProxyRuntimeCooling(proxy.ID, now) {
			continue
		}
		info, ok := proxyIndex[proxy.ID]
		if !ok || strings.TrimSpace(info.URL) == "" || strings.TrimSpace(info.URL) == failedProxyURL {
			continue
		}
		cand := candidate{info: info, id: proxy.ID, success: true, latencyMs: 1<<62 - 1}
		if proxy.LastTest != nil {
			cand.success = proxy.LastTest.Success
			if proxy.LastTest.ResponseTime > 0 {
				cand.latencyMs = proxy.LastTest.ResponseTime
			}
		}
		candidates = append(candidates, cand)
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].success != candidates[j].success {
			return candidates[i].success && !candidates[j].success
		}
		if candidates[i].latencyMs != candidates[j].latencyMs {
			return candidates[i].latencyMs < candidates[j].latencyMs
		}
		return candidates[i].id < candidates[j].id
	})
	return candidates[0].info.URL, true
}

func currentSystemProxyURL(cfg models.SystemConfig) string {
	if info, ok := resolveSystemProxyRuntimeInfo(cfg); ok && strings.TrimSpace(info.URL) != "" {
		return strings.TrimSpace(info.URL)
	}
	return strings.TrimSpace(cfg.UpstreamProxyURL)
}

func markProxyRuntimeCooling(proxyID uint, duration time.Duration) {
	if proxyID == 0 {
		return
	}
	if duration <= 0 {
		duration = 10 * time.Minute
	}
	upstreamRuntimeMu.Lock()
	upstreamRuntimeCoolingByProxy[proxyID] = time.Now().Add(duration)
	upstreamRuntimeMu.Unlock()
}

func isProxyRuntimeCooling(proxyID uint, now time.Time) bool {
	if proxyID == 0 {
		return false
	}
	upstreamRuntimeMu.Lock()
	defer upstreamRuntimeMu.Unlock()
	until, ok := upstreamRuntimeCoolingByProxy[proxyID]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(upstreamRuntimeCoolingByProxy, proxyID)
		return false
	}
	return true
}

func setProxyRuntimeFailover(origin, replacement upstreamProxyRuntimeInfo) {
	originURL := proxyRuntimeURLKey(origin.URL)
	if origin.ID == 0 && originURL == "" {
		return
	}
	if resolved, ok := lookupProxyRuntimeInfo(replacement.ID); ok && strings.TrimSpace(resolved.URL) != "" {
		replacement = resolved
	}
	upstreamRuntimeMu.Lock()
	defer upstreamRuntimeMu.Unlock()
	if origin.ID > 0 {
		upstreamRuntimeFailoverByID[origin.ID] = replacement
		upstreamRuntimeCoolingByProxy[origin.ID] = time.Now().Add(10 * time.Minute)
	}
	if originURL != "" {
		upstreamRuntimeFailoverByURL[originURL] = replacement
	}
	for id, info := range upstreamRuntimeFailoverByID {
		if info.ID == origin.ID || (originURL != "" && proxyRuntimeURLKey(info.URL) == originURL) {
			upstreamRuntimeFailoverByID[id] = replacement
		}
	}
	for rawURL, info := range upstreamRuntimeFailoverByURL {
		if info.ID == origin.ID || (originURL != "" && proxyRuntimeURLKey(info.URL) == originURL) {
			upstreamRuntimeFailoverByURL[rawURL] = replacement
		}
	}
}

type managedProxyCandidate struct {
	info        upstreamProxyRuntimeInfo
	successRank int
	latencyMs   int64
	id          uint
}

func selectAlternativeManagedProxyFromStore(store *db.Store, origin upstreamProxyRuntimeInfo) (upstreamProxyRuntimeInfo, bool) {
	if store == nil {
		return upstreamProxyRuntimeInfo{}, false
	}
	origin = upstreamProxyRuntimeInfo{ID: origin.ID, URL: strings.TrimSpace(origin.URL), ManagedBy: strings.TrimSpace(origin.ManagedBy)}
	if origin.ManagedBy != models.CoreManagedByXray {
		return upstreamProxyRuntimeInfo{}, false
	}
	proxyIndex := buildProxyRuntimeIndex(store.Proxies)
	now := time.Now()
	candidates := make([]managedProxyCandidate, 0)
	for _, proxy := range store.Proxies {
		proxy = models.NormalizeUpstreamProxy(proxy)
		if proxy.ID == 0 || proxy.ID == origin.ID {
			continue
		}
		if proxy.ManagedBy != models.CoreManagedByXray || !isProxyEnabled(proxy) {
			continue
		}
		if isProxyRuntimeCooling(proxy.ID, now) {
			continue
		}
		info, ok := proxyIndex[proxy.ID]
		if !ok || strings.TrimSpace(info.URL) == "" {
			continue
		}
		successRank := 1
		latencyMs := int64(1 << 62)
		if proxy.LastTest != nil {
			if proxy.LastTest.Success {
				successRank = 3
			} else {
				successRank = 0
			}
			if proxy.LastTest.ResponseTime > 0 {
				latencyMs = proxy.LastTest.ResponseTime
			}
		}
		candidates = append(candidates, managedProxyCandidate{info: info, successRank: successRank, latencyMs: latencyMs, id: proxy.ID})
	}
	if len(candidates) == 0 {
		return upstreamProxyRuntimeInfo{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].successRank != candidates[j].successRank {
			return candidates[i].successRank > candidates[j].successRank
		}
		if candidates[i].latencyMs != candidates[j].latencyMs {
			return candidates[i].latencyMs < candidates[j].latencyMs
		}
		return candidates[i].id < candidates[j].id
	})
	return candidates[0].info, true
}

func promoteAlternativeManagedProxy(origin upstreamProxyRuntimeInfo) (upstreamProxyRuntimeInfo, bool) {
	store, err := db.ReadStore()
	if err != nil {
		return upstreamProxyRuntimeInfo{}, false
	}
	replacement, ok := selectAlternativeManagedProxyFromStore(store, origin)
	if !ok {
		return upstreamProxyRuntimeInfo{}, false
	}
	setProxyRuntimeFailover(origin, replacement)
	return replacement, true
}

func resolveProxyOverrideForPlaintextKey(plaintextKey string) (string, bool) {
	plaintextKey = strings.TrimSpace(plaintextKey)
	if plaintextKey == "" {
		return "", false
	}
	if info, ok := lookupKeyRuntimeInfo(plaintextKey); ok {
		if proxyInfo, found := lookupProxyRuntimeInfo(info.ProxyID); found && strings.TrimSpace(proxyInfo.URL) != "" {
			return strings.TrimSpace(proxyInfo.URL), true
		}
		if strings.TrimSpace(info.ProxyURL) != "" {
			return strings.TrimSpace(info.ProxyURL), true
		}
	}
	return resolveProxyOverrideForPlaintextKeyFromStore(plaintextKey)
}

func resolveProxyOverrideForPlaintextKeyFromStore(plaintextKey string) (string, bool) {
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		return "", false
	}
	store, err := db.ReadStore()
	if err != nil {
		return "", false
	}
	proxyIndex := buildProxyRuntimeIndex(store.Proxies)
	for _, key := range store.APIKeys {
		plaintext, decryptErr := utils.Decrypt(key.Key, secret)
		if decryptErr != nil || plaintext != plaintextKey {
			continue
		}
		proxyInfo, ok := proxyIndex[key.ProxyID]
		if !ok || proxyInfo.URL == "" {
			return "", false
		}
		return proxyInfo.URL, true
	}
	return "", false
}

func lookupKeyRuntimeInfo(plaintextKey string) (upstreamKeyRuntimeInfo, bool) {
	plaintextKey = strings.TrimSpace(plaintextKey)
	if plaintextKey == "" {
		return upstreamKeyRuntimeInfo{}, false
	}
	upstreamRuntimeMu.RLock()
	info, ok := upstreamRuntimeByKey[plaintextKey]
	upstreamRuntimeMu.RUnlock()
	if ok {
		return info, true
	}
	return upstreamKeyRuntimeInfo{}, false
}

func lookupProxyRuntimeInfo(proxyID uint) (upstreamProxyRuntimeInfo, bool) {
	if proxyID == 0 {
		return upstreamProxyRuntimeInfo{}, false
	}
	upstreamRuntimeMu.RLock()
	if override, ok := upstreamRuntimeFailoverByID[proxyID]; ok && strings.TrimSpace(override.URL) != "" {
		upstreamRuntimeMu.RUnlock()
		return override, true
	}
	info, ok := upstreamRuntimeByProxy[proxyID]
	upstreamRuntimeMu.RUnlock()
	if ok {
		return info, true
	}
	return upstreamProxyRuntimeInfo{}, false
}

func resolveSystemProxyRuntimeInfo(cfg models.SystemConfig) (upstreamProxyRuntimeInfo, bool) {
	cfg = models.NormalizeSystemConfig(cfg)
	if cfg.UpstreamProxyID > 0 {
		if info, ok := lookupProxyRuntimeInfo(cfg.UpstreamProxyID); ok {
			return info, true
		}
	}
	proxyURL := proxyRuntimeURLKey(cfg.UpstreamProxyURL)
	if proxyURL == "" {
		return upstreamProxyRuntimeInfo{}, false
	}
	upstreamRuntimeMu.RLock()
	if override, ok := upstreamRuntimeFailoverByURL[proxyURL]; ok && strings.TrimSpace(override.URL) != "" {
		upstreamRuntimeMu.RUnlock()
		return override, true
	}
	for _, info := range upstreamRuntimeByProxy {
		if proxyRuntimeURLKey(info.URL) == proxyURL {
			upstreamRuntimeMu.RUnlock()
			if resolved, ok := lookupProxyRuntimeInfo(info.ID); ok {
				return resolved, true
			}
			return info, true
		}
	}
	upstreamRuntimeMu.RUnlock()
	return upstreamProxyRuntimeInfo{URL: proxyURL}, true
}

func effectiveProxyRuntimeInfoForAPIKey(cfg models.SystemConfig, plaintextKey string) (upstreamProxyRuntimeInfo, bool) {
	if info, ok := lookupKeyRuntimeInfo(plaintextKey); ok {
		if proxyInfo, found := lookupProxyRuntimeInfo(info.ProxyID); found {
			return proxyInfo, true
		}
		if strings.TrimSpace(info.ProxyURL) != "" {
			return upstreamProxyRuntimeInfo{ID: info.ProxyID, Name: info.ProxyName, URL: info.ProxyURL}, true
		}
	}
	return resolveSystemProxyRuntimeInfo(cfg)
}

func effectiveProxyURLForAPIKey(cfg models.SystemConfig, plaintextKey string) string {
	if info, ok := effectiveProxyRuntimeInfoForAPIKey(cfg, plaintextKey); ok && strings.TrimSpace(info.URL) != "" {
		return strings.TrimSpace(info.URL)
	}
	return strings.TrimSpace(cfg.UpstreamProxyURL)
}

func buildProxyReferenceIndex(proxies []models.UpstreamProxy) map[uint]models.UpstreamProxy {
	items := make(map[uint]models.UpstreamProxy, len(proxies))
	for _, proxy := range proxies {
		proxy = models.NormalizeUpstreamProxy(proxy)
		if proxy.ID == 0 {
			continue
		}
		items[proxy.ID] = proxy
	}
	return items
}

func countAPIKeyProxyUsage(keys []models.APIKey) map[uint]int {
	counts := make(map[uint]int)
	for _, key := range keys {
		if key.ProxyID == 0 {
			continue
		}
		counts[key.ProxyID]++
	}
	return counts
}

func validateAPIKeyProxyReference(store *db.Store, proxyID uint) error {
	if proxyID == 0 {
		return nil
	}
	for _, proxy := range store.Proxies {
		if proxy.ID == proxyID {
			return nil
		}
	}
	return fmt.Errorf("所选代理不存在")
}

func newHTTPClientWithProxyOverrideAndTimeout(cfg models.SystemConfig, overrideProxyURL *string, disableTotalTimeout bool) *http.Client {
	timeout := time.Duration(cfg.RequestTimeoutSecond) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if disableTotalTimeout {
		timeout = 0
	}
	effectiveProxyURL := currentSystemProxyURL(cfg)
	if overrideProxyURL != nil {
		effectiveProxyURL = strings.TrimSpace(*overrideProxyURL)
	}
	transport, err := cachedTransportForProxySetting(cfg, effectiveProxyURL, disableTotalTimeout)
	if err != nil || transport == nil {
		transport = cloneDefaultTransport()
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func newHTTPClientWithProxyOverride(cfg models.SystemConfig, overrideProxyURL *string) *http.Client {
	return newHTTPClientWithProxyOverrideAndTimeout(cfg, overrideProxyURL, false)
}

func newStreamHTTPClientWithProxyOverride(cfg models.SystemConfig, overrideProxyURL *string) *http.Client {
	return newHTTPClientWithProxyOverrideAndTimeout(cfg, overrideProxyURL, true)
}

func newHTTPClientForAPIKey(cfg models.SystemConfig, plaintextKey string) *http.Client {
	if info, ok := effectiveProxyRuntimeInfoForAPIKey(cfg, plaintextKey); ok && strings.TrimSpace(info.URL) != "" {
		proxyURL := strings.TrimSpace(info.URL)
		return newHTTPClientWithProxyOverride(cfg, &proxyURL)
	}
	return newHTTPClient(cfg)
}

func newStreamHTTPClientForAPIKey(cfg models.SystemConfig, plaintextKey string) *http.Client {
	if info, ok := effectiveProxyRuntimeInfoForAPIKey(cfg, plaintextKey); ok && strings.TrimSpace(info.URL) != "" {
		proxyURL := strings.TrimSpace(info.URL)
		return newStreamHTTPClientWithProxyOverride(cfg, &proxyURL)
	}
	return newStreamHTTPClientWithProxyOverride(cfg, nil)
}

func testUpstreamProxyConnectivity(ctx context.Context, proxyCfg models.UpstreamProxy) map[string]any {
	cfg := loadSystemConfig()
	proxyURL, err := buildProxyURLFromModel(proxyCfg)
	if err != nil {
		return map[string]any{
			"success": false,
			"message": err.Error(),
		}
	}
	client := newHTTPClientWithProxyOverride(cfg, &proxyURL)
	startedAt := time.Now()
	requestURL := buildUpstreamURL(cfg, "models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return map[string]any{"success": false, "message": err.Error()}
	}
	resp, err := client.Do(req)
	durationMs := time.Since(startedAt).Milliseconds()
	if err != nil {
		return map[string]any{
			"success":       false,
			"proxy_id":      proxyCfg.ID,
			"proxy_type":    proxyCfg.Type,
			"response_time": durationMs,
			"target":        requestURL,
			"message":       err.Error(),
		}
	}
	defer resp.Body.Close()
	success := resp.StatusCode >= 200 && resp.StatusCode < 500
	message := fmt.Sprintf("代理可达，上游返回 HTTP %d", resp.StatusCode)
	if !success {
		message = fmt.Sprintf("代理建立成功，但上游返回 HTTP %d", resp.StatusCode)
	}
	return map[string]any{
		"success":       success,
		"proxy_id":      proxyCfg.ID,
		"proxy_type":    proxyCfg.Type,
		"response_time": durationMs,
		"status_code":   resp.StatusCode,
		"target":        requestURL,
		"message":       message,
	}
}
