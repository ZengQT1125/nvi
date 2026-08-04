package gateway

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"
	"nvidia-api-gateway/pkg/utils"
)

type xrayRuntimeSnapshot struct {
	Running         bool      `json:"running"`
	Platform        string    `json:"platform"`
	BinaryPath      string    `json:"binaryPath,omitempty"`
	ConfigPath      string    `json:"configPath,omitempty"`
	LogPath         string    `json:"logPath,omitempty"`
	Version         string    `json:"version,omitempty"`
	EnabledProfiles int       `json:"enabledProfiles"`
	ManagedProxies  int       `json:"managedProxies"`
	LastAppliedAt   time.Time `json:"lastAppliedAt,omitempty"`
	StartedAt       time.Time `json:"startedAt,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	ActivePorts     []int     `json:"activePorts,omitempty"`
}

type xrayResolvedProfile struct {
	Profile         models.CoreProfile
	PlainAuthID     string
	PlainPassword   string
	LocalListenHost string
}

type xrayConfigAsset struct {
	Version string
	URL     string
	Name    string
}

type githubReleaseResponse struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type XrayCoreManager struct {
	sched *scheduler.Scheduler

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	runtime  xrayRuntimeSnapshot
}

func NewXrayCoreManager(sched *scheduler.Scheduler) *XrayCoreManager {
	return &XrayCoreManager{
		sched: sched,
		runtime: xrayRuntimeSnapshot{
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
		},
	}
}

func (m *XrayCoreManager) Start(ctx context.Context) {
	_ = m.Reload(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = m.Stop(context.Background())
			return
		case <-ticker.C:
			_ = m.Ensure(ctx)
		}
	}
}

func (m *XrayCoreManager) Snapshot() xrayRuntimeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.runtime
	snapshot.ActivePorts = append([]int(nil), m.runtime.ActivePorts...)
	return snapshot
}

func (m *XrayCoreManager) ClearLogs() (string, error) {
	logPath := strings.TrimSpace(m.Snapshot().LogPath)
	if logPath == "" {
		logPath = utils.ResolveXrayLogPath()
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.runtime.LogPath = logPath
	m.runtime.LastError = ""
	m.mu.Unlock()
	return logPath, nil
}

func (m *XrayCoreManager) Stop(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *XrayCoreManager) Ensure(ctx context.Context) error {
	return m.reconcile(ctx, false)
}

func (m *XrayCoreManager) Reload(ctx context.Context) error {
	return m.reconcile(ctx, true)
}

func (m *XrayCoreManager) reconcile(ctx context.Context, forceRestart bool) error {
	profiles, managedCount, err := syncCoreProfilesForRuntime()
	if err != nil {
		m.setRuntimeError(err)
		return err
	}
	if err := LoadActiveKeys(ctx, m.sched); err != nil {
		m.setRuntimeError(err)
		return err
	}
	resolved, err := resolveEnabledCoreProfiles(profiles)
	if err != nil {
		m.setRuntimeError(err)
		return err
	}
	if len(resolved) == 0 {
		m.mu.Lock()
		_ = m.stopLocked()
		m.runtime.EnabledProfiles = 0
		m.runtime.ManagedProxies = managedCount
		m.runtime.ActivePorts = make([]int, 0)
		m.mu.Unlock()
		return nil
	}
	binaryPath, version, err := ensureXrayBinary(ctx)
	if err != nil {
		m.setRuntimeError(err)
		return err
	}
	configPath, logPath, err := buildXrayRuntimeConfig(resolved)
	if err != nil {
		m.setRuntimeError(err)
		return err
	}
	ports := make([]int, 0, len(resolved))
	for _, item := range resolved {
		ports = append(ports, item.Profile.LocalPort)
	}
	sort.Ints(ports)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime.Platform = runtime.GOOS + "/" + runtime.GOARCH
	m.runtime.BinaryPath = binaryPath
	m.runtime.ConfigPath = configPath
	m.runtime.LogPath = logPath
	m.runtime.Version = version
	m.runtime.EnabledProfiles = len(resolved)
	m.runtime.ManagedProxies = managedCount
	m.runtime.ActivePorts = ports
	if m.cmd != nil && m.cmd.Process != nil && !forceRestart {
		if m.runtime.Running && localPortsReachable(ports, 300*time.Millisecond) {
			return nil
		}
	}
	if err := m.stopLocked(); err != nil {
		return err
	}
	// xray 重启前清除所有本地回环代理的 transport 缓存，
	// 避免旧连接池复用已断开的 SOCKS5 连接导致请求失败
	invalidateLoopbackTransportCache()
	if err := m.startLocked(binaryPath, configPath, logPath); err != nil {
		m.runtime.LastError = err.Error()
		return err
	}
	m.runtime.LastError = ""
	m.runtime.LastAppliedAt = time.Now()
	return nil
}

func (m *XrayCoreManager) startLocked(binaryPath, configPath, logPath string) error {
	if strings.TrimSpace(binaryPath) == "" {
		return errors.New("xray binary path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(binaryPath, "run", "-c", configPath)
	cmd.Dir = filepath.Dir(binaryPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	m.cmd = cmd
	m.stopping = false
	m.runtime.Running = false
	go m.waitCommand(cmd, logFile)
	if err := waitForLocalPortsReady(m.runtime.ActivePorts, 10*time.Second); err != nil {
		m.runtime.LastError = err.Error()
		m.stopping = true
		_ = cmd.Process.Kill()
		return err
	}
	m.runtime.Running = true
	m.runtime.StartedAt = time.Now()
	return nil
}

func (m *XrayCoreManager) waitCommand(cmd *exec.Cmd, logFile *os.File) {
	err := cmd.Wait()
	_ = logFile.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != cmd {
		return
	}
	m.cmd = nil
	m.runtime.Running = false
	if err != nil && !m.stopping {
		m.runtime.LastError = err.Error()
	}
	m.stopping = false
}

func localPortsReachable(ports []int, dialTimeout time.Duration) bool {
	if len(ports) == 0 {
		return false
	}
	if dialTimeout <= 0 {
		dialTimeout = 300 * time.Millisecond
	}
	for _, port := range ports {
		address := net.JoinHostPort(models.CoreLocalHost, fmt.Sprintf("%d", port))
		conn, err := net.DialTimeout("tcp", address, dialTimeout)
		if err != nil {
			return false
		}
		_ = conn.Close()
	}
	return true
}

func waitForLocalPortsReady(ports []int, timeout time.Duration) error {
	if len(ports) == 0 {
		return errors.New("xray runtime has no local ports to validate")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if localPortsReachable(ports, 250*time.Millisecond) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("xray local listeners did not become ready within %s", timeout)
}

func (m *XrayCoreManager) stopLocked() error {
	if m.cmd == nil || m.cmd.Process == nil {
		m.runtime.Running = false
		m.stopping = false
		return nil
	}
	m.stopping = true
	err := m.cmd.Process.Kill()
	m.runtime.Running = false
	return err
}

func (m *XrayCoreManager) setRuntimeError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime.LastError = err.Error()
}

func syncCoreProfilesForRuntime() ([]models.CoreProfile, int, error) {
	profiles := make([]models.CoreProfile, 0)
	managedCount := 0
	err := db.UpdateStore(func(store *db.Store) error {
		store.CoreProfiles = disableIncompatibleCoreProfiles(normalizeAndAllocateCoreProfiles(store.CoreProfiles))
		managedCount = syncManagedCoreProxies(store)
		profiles = append([]models.CoreProfile(nil), store.CoreProfiles...)
		return nil
	})
	return profiles, managedCount, err
}

func disableIncompatibleCoreProfiles(items []models.CoreProfile) []models.CoreProfile {
	normalized := append([]models.CoreProfile(nil), items...)
	now := time.Now()
	for i := range normalized {
		normalized[i] = models.NormalizeCoreProfile(normalized[i])
		reason := incompatibleCoreProfileReason(normalized[i])
		if reason == "" || normalized[i].Status == models.CoreProfileStatusDisabled {
			continue
		}
		normalized[i].Status = models.CoreProfileStatusDisabled
		normalized[i].UpdatedAt = now
		normalized[i].Remarks = appendCoreProfileRemark(normalized[i].Remarks, "自动禁用："+reason)
	}
	return normalized
}

func incompatibleCoreProfileReason(profile models.CoreProfile) string {
	profile = models.NormalizeCoreProfile(profile)
	switch profile.Protocol {
	case "shadowsocks":
		if profile.Method != "" && !models.IsSupportedShadowsocksMethod(profile.Method) {
			return fmt.Sprintf("Shadowsocks 加密方法 %q 不受当前 Xray 支持", profile.Method)
		}
	}
	return ""
}

func appendCoreProfileRemark(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if note == "" {
		return existing
	}
	if existing == "" {
		return note
	}
	if strings.Contains(existing, note) {
		return existing
	}
	return existing + " | " + note
}

func normalizeAndAllocateCoreProfiles(items []models.CoreProfile) []models.CoreProfile {
	used := make(map[int]uint)
	for i := range items {
		items[i] = models.NormalizeCoreProfile(items[i])
		if items[i].LocalPort > 0 {
			used[items[i].LocalPort] = items[i].ID
		}
	}
	nextPort := models.CoreLocalPortStart
	for i := range items {
		current := items[i].LocalPort
		if current > 0 {
			owner := used[current]
			if owner == 0 || owner == items[i].ID {
				continue
			}
		}
		for {
			if nextPort < models.CoreLocalPortStart {
				nextPort = models.CoreLocalPortStart
			}
			if _, exists := used[nextPort]; !exists {
				items[i].LocalPort = nextPort
				used[nextPort] = items[i].ID
				nextPort++
				break
			}
			nextPort++
		}
	}
	return items
}

func syncManagedCoreProxies(store *db.Store) int {
	if store == nil {
		return 0
	}
	proxyIndexByID := make(map[uint]int, len(store.Proxies))
	managedIndexByProfile := make(map[uint]int)
	for i := range store.Proxies {
		proxyIndexByID[store.Proxies[i].ID] = i
		if store.Proxies[i].ManagedBy == models.CoreManagedByXray && store.Proxies[i].ManagedRefID > 0 {
			managedIndexByProfile[store.Proxies[i].ManagedRefID] = i
		}
	}
	activeProfiles := make(map[uint]struct{}, len(store.CoreProfiles))
	for i := range store.CoreProfiles {
		profile := models.NormalizeCoreProfile(store.CoreProfiles[i])
		activeProfiles[profile.ID] = struct{}{}
		proxyIdx := -1
		if profile.ManagedProxyID > 0 {
			if idx, ok := proxyIndexByID[profile.ManagedProxyID]; ok {
				proxyIdx = idx
			}
		}
		if proxyIdx == -1 {
			if idx, ok := managedIndexByProfile[profile.ID]; ok {
				proxyIdx = idx
			}
		}
		if proxyIdx == -1 {
			proxy := models.NormalizeUpstreamProxy(models.UpstreamProxy{
				ID:           store.NextProxyID,
				Name:         buildManagedCoreProxyName(profile.Name),
				Group:        "XRAY",
				Source:       "managed",
				ManagedBy:    models.CoreManagedByXray,
				ManagedRefID: profile.ID,
				Type:         "dokodemo",
				Status:       managedCoreProxyStatus(profile),
				Host:         models.CoreLocalHost,
				Port:         profile.LocalPort,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
				TestHistory:  make([]models.ProxyTestRecord, 0),
			})
			store.NextProxyID++
			store.Proxies = append(store.Proxies, proxy)
			proxyIdx = len(store.Proxies) - 1
		}
		proxy := store.Proxies[proxyIdx]
		proxy.Name = buildManagedCoreProxyName(profile.Name)
		proxy.Group = "XRAY"
		proxy.Source = "managed"
		proxy.ManagedBy = models.CoreManagedByXray
		proxy.ManagedRefID = profile.ID
		proxy.Type = "dokodemo"
		proxy.Status = managedCoreProxyStatus(profile)
		proxy.Host = models.CoreLocalHost
		proxy.Port = profile.LocalPort
		proxy.UpdatedAt = time.Now()
		store.Proxies[proxyIdx] = models.NormalizeUpstreamProxy(proxy)
		store.CoreProfiles[i].ManagedProxyID = store.Proxies[proxyIdx].ID
	}
	if len(store.Proxies) > 0 {
		filtered := make([]models.UpstreamProxy, 0, len(store.Proxies))
		removedProxyIDs := make(map[uint]struct{})
		for _, proxy := range store.Proxies {
			if proxy.ManagedBy == models.CoreManagedByXray {
				if _, ok := activeProfiles[proxy.ManagedRefID]; !ok {
					removedProxyIDs[proxy.ID] = struct{}{}
					continue
				}
			}
			filtered = append(filtered, proxy)
		}
		if len(removedProxyIDs) > 0 {
			for i := range store.APIKeys {
				if _, ok := removedProxyIDs[store.APIKeys[i].ProxyID]; ok {
					store.APIKeys[i].ProxyID = 0
					store.APIKeys[i].UpdatedAt = time.Now()
				}
			}
			store.Proxies = filtered
		}
	}
	count := 0
	for _, proxy := range store.Proxies {
		if proxy.ManagedBy == models.CoreManagedByXray {
			count++
		}
	}
	return count
}

func buildManagedCoreProxyName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "未命名节点"
	}
	return "[XRAY] " + trimmed
}

func managedCoreProxyStatus(profile models.CoreProfile) string {
	if profile.Status == models.CoreProfileStatusDisabled {
		return models.ProxyStatusDisabled
	}
	return models.ProxyStatusEnabled
}

func resolveEnabledCoreProfiles(items []models.CoreProfile) ([]xrayResolvedProfile, error) {
	resolved := make([]xrayResolvedProfile, 0)
	for _, item := range items {
		item = models.NormalizeCoreProfile(item)
		if item.Status != models.CoreProfileStatusEnabled {
			continue
		}
		plainAuthID, err := decryptCoreSecret(item.AuthID)
		if err != nil {
			return nil, err
		}
		plainPassword, err := decryptCoreSecret(item.Password)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, xrayResolvedProfile{
			Profile:         item,
			PlainAuthID:     plainAuthID,
			PlainPassword:   plainPassword,
			LocalListenHost: models.CoreLocalHost,
		})
	}
	return resolved, nil
}

func encryptCoreSecret(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		return "", errors.New("missing ENCRYPTION_KEY")
	}
	return utils.Encrypt(trimmed, secret)
}

func decryptCoreSecret(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	secret := strings.TrimSpace(utils.GetEncryptionKey())
	if secret == "" || len(secret) != 32 {
		return "", errors.New("missing ENCRYPTION_KEY")
	}
	return utils.Decrypt(trimmed, secret)
}

func xrayWorkingDir() string {
	return utils.ResolveXrayCoreDir()
}

func legacyXrayDirs() []string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0, 5)
	appendDir := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		dirs = append(dirs, cleaned)
	}
	projectRoot := utils.ResolveProjectRoot()
	appendDir(filepath.Join(projectRoot, "runtime", "xray"))
	appendDir(filepath.Join(projectRoot, "xray"))
	dataRoot := utils.ResolveGatewayDataDir()
	appendDir(filepath.Join(dataRoot, "runtime", "xray"))
	appendDir(filepath.Join(dataRoot, "xray"))
	if exePath, err := os.Executable(); err == nil {
		appendDir(filepath.Join(filepath.Dir(exePath), "runtime", "xray"))
	}
	return dirs
}

func legacyXrayBinaryCandidates() []string {
	items := make([]string, 0, len(legacyXrayDirs()))
	for _, dir := range legacyXrayDirs() {
		items = append(items, filepath.Join(dir, xrayBinaryName()))
	}
	return items
}

func xrayBinaryName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

func xrayBinaryPath() string {
	if custom := strings.TrimSpace(os.Getenv("XRAY_BIN_PATH")); custom != "" {
		return utils.ResolveAbsolutePath(custom)
	}
	return filepath.Join(xrayWorkingDir(), xrayBinaryName())
}

func inspectXrayBinary(binaryPath string) (string, error) {
	info, err := os.Stat(binaryPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("xray binary path is a directory: %s", binaryPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "version")
	cmd.Dir = filepath.Dir(binaryPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return "", fmt.Errorf("verify xray binary: %w (%s)", err, trimmed)
		}
		return "", fmt.Errorf("verify xray binary: %w", err)
	}
	version := parseXrayVersionOutput(output)
	if version == "" {
		return "", errors.New("xray version output is empty")
	}
	return version, nil
}

func parseXrayVersionOutput(output []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func seedXrayRuntimeFromLegacyBinary(binaryPath string) (string, error) {
	for _, candidate := range legacyXrayBinaryCandidates() {
		if strings.EqualFold(filepath.Clean(candidate), filepath.Clean(binaryPath)) {
			continue
		}
		version, err := inspectXrayBinary(candidate)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(xrayWorkingDir(), 0o755); err != nil {
			return "", err
		}
		for _, name := range []string{xrayBinaryName(), "geoip.dat", "geosite.dat"} {
			sourcePath := filepath.Join(filepath.Dir(candidate), name)
			if _, statErr := os.Stat(sourcePath); statErr != nil {
				continue
			}
			targetPath := filepath.Join(xrayWorkingDir(), name)
			if err := copyFile(sourcePath, targetPath); err != nil {
				return "", err
			}
		}
		if _, err := inspectXrayBinary(binaryPath); err == nil {
			return version, nil
		}
	}
	return "", os.ErrNotExist
}

func copyFile(sourcePath, targetPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return err
	}
	if runtime.GOOS != "windows" && strings.EqualFold(filepath.Base(targetPath), xrayBinaryName()) {
		_ = os.Chmod(targetPath, 0o755)
	}
	return nil
}

func extractXrayBinaryFromLocalArchives(binaryPath string, asset xrayConfigAsset) (string, error) {
	candidates, err := xrayArchiveCandidates(asset.Name)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	var lastErr error
	for _, archivePath := range candidates {
		if err := unzipXrayArchive(archivePath, xrayWorkingDir()); err != nil {
			lastErr = err
			continue
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(binaryPath, 0o755)
		}
		if version, err := inspectXrayBinary(binaryPath); err == nil {
			return version, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no usable xray archive found")
	}
	return "", lastErr
}

func xrayArchiveCandidates(preferred string) ([]string, error) {
	type candidate struct {
		path    string
		name    string
		prefer  int
		modTime time.Time
	}
	items := make([]candidate, 0)
	seen := make(map[string]struct{})
	searchDirs := append([]string{xrayWorkingDir()}, legacyXrayDirs()...)
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if !strings.HasSuffix(strings.ToLower(name), ".zip") {
				continue
			}
			fullPath := filepath.Join(dir, name)
			if _, ok := seen[strings.ToLower(fullPath)]; ok {
				continue
			}
			seen[strings.ToLower(fullPath)] = struct{}{}
			prefer := 10
			switch {
			case strings.EqualFold(name, preferred):
				prefer = 0
			case strings.EqualFold(name, "api_asset_download.zip"):
				prefer = 1
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			items = append(items, candidate{path: fullPath, name: name, prefer: prefer, modTime: info.ModTime()})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].prefer != items[j].prefer {
			return items[i].prefer < items[j].prefer
		}
		if !items[i].modTime.Equal(items[j].modTime) {
			return items[i].modTime.After(items[j].modTime)
		}
		return strings.ToLower(items[i].name) < strings.ToLower(items[j].name)
	})
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.path)
	}
	return paths, nil
}

func xrayDownloadURLs(asset xrayConfigAsset) []string {
	urls := make([]string, 0, 4)
	for _, raw := range strings.Split(strings.TrimSpace(os.Getenv("XRAY_DOWNLOAD_URLS")), ",") {
		if expanded := expandXrayDownloadURLTemplate(raw, asset); expanded != "" {
			urls = append(urls, expanded)
		}
	}
	if strings.TrimSpace(asset.URL) != "" {
		urls = append(urls, strings.TrimSpace(asset.URL))
	}
	seen := make(map[string]struct{}, len(urls))
	filtered := make([]string, 0, len(urls))
	for _, item := range urls {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

func expandXrayDownloadURLTemplate(raw string, asset xrayConfigAsset) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"{name}", asset.Name,
		"{asset}", asset.Name,
		"{tag}", asset.Version,
		"{version}", asset.Version,
	)
	return strings.TrimSpace(replacer.Replace(trimmed))
}

func ensureXrayBinary(ctx context.Context) (string, string, error) {
	binaryPath := xrayBinaryPath()
	configuredVersion := strings.TrimSpace(os.Getenv("XRAY_VERSION"))
	if version, err := inspectXrayBinary(binaryPath); err == nil {
		return binaryPath, firstNonEmpty(version, configuredVersion), nil
	}
	if version, err := seedXrayRuntimeFromLegacyBinary(binaryPath); err == nil {
		return binaryPath, firstNonEmpty(version, configuredVersion), nil
	}
	assetName, err := xrayAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(xrayWorkingDir(), 0o755); err != nil {
		return "", "", err
	}
	seedAsset := xrayConfigAsset{Version: configuredVersion, Name: assetName}
	if version, err := extractXrayBinaryFromLocalArchives(binaryPath, seedAsset); err == nil {
		return binaryPath, firstNonEmpty(version, configuredVersion), nil
	}
	if version, err := downloadAndPrepareXrayBinary(ctx, binaryPath, seedAsset); err == nil {
		return binaryPath, firstNonEmpty(version, configuredVersion), nil
	}
	asset, err := resolveXrayAsset(ctx)
	if err != nil {
		return "", "", err
	}
	version, err := downloadAndPrepareXrayBinary(ctx, binaryPath, asset)
	if err != nil {
		return "", "", err
	}
	return binaryPath, firstNonEmpty(version, asset.Version, configuredVersion), nil
}

func downloadAndPrepareXrayBinary(ctx context.Context, binaryPath string, asset xrayConfigAsset) (string, error) {
	archiveName := strings.TrimSpace(asset.Name)
	if archiveName == "" {
		return "", errors.New("xray archive name is empty")
	}
	archivePath := filepath.Join(xrayWorkingDir(), archiveName)
	urls := xrayDownloadURLs(asset)
	if len(urls) == 0 {
		return "", errors.New("no xray download URLs available")
	}
	var lastErr error
	for _, downloadURL := range urls {
		if err := downloadXrayArchive(ctx, downloadURL, archivePath); err != nil {
			lastErr = err
			continue
		}
		if err := unzipXrayArchive(archivePath, xrayWorkingDir()); err != nil {
			lastErr = err
			continue
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(binaryPath, 0o755)
		}
		if version, err := inspectXrayBinary(binaryPath); err == nil {
			return version, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unable to prepare xray binary")
	}
	return "", lastErr
}

func resolveXrayAsset(ctx context.Context) (xrayConfigAsset, error) {
	assetName, err := xrayAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return xrayConfigAsset{}, err
	}
	version := strings.TrimSpace(os.Getenv("XRAY_VERSION"))
	apiURL := "https://api.github.com/repos/XTLS/Xray-core/releases/latest"
	if version != "" && !strings.EqualFold(version, "latest") {
		apiURL = "https://api.github.com/repos/XTLS/Xray-core/releases/tags/" + version
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return xrayConfigAsset{}, err
	}
	req.Header.Set("User-Agent", "nvidia-api-gateway")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return xrayConfigAsset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return xrayConfigAsset{}, fmt.Errorf("resolve xray release failed: http %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var release githubReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return xrayConfigAsset{}, err
	}
	for _, item := range release.Assets {
		if strings.EqualFold(strings.TrimSpace(item.Name), assetName) {
			return xrayConfigAsset{Version: release.TagName, URL: item.BrowserDownloadURL, Name: item.Name}, nil
		}
	}
	return xrayConfigAsset{}, fmt.Errorf("xray asset %s not found in release %s", assetName, release.TagName)
}

func xrayAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "Xray-windows-64.zip", nil
		case "386":
			return "Xray-windows-32.zip", nil
		case "arm64":
			return "Xray-windows-arm64-v8a.zip", nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "Xray-linux-64.zip", nil
		case "386":
			return "Xray-linux-32.zip", nil
		case "arm64":
			return "Xray-linux-arm64-v8a.zip", nil
		case "arm":
			return "Xray-linux-arm32-v7a.zip", nil
		}
	}
	return "", fmt.Errorf("unsupported xray platform: %s/%s", goos, goarch)
}

func downloadXrayArchive(ctx context.Context, downloadURL, target string) (err error) {
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nvidia-api-gateway")
	req.Header.Set("Accept", "application/octet-stream")
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download xray failed from %s: http %d %s", downloadURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	partPath := target + ".part"
	if removeErr := os.Remove(partPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	file, err := os.Create(partPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(partPath)
		}
	}()
	if _, err = io.Copy(file, resp.Body); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = validateXrayArchive(partPath); err != nil {
		return fmt.Errorf("downloaded archive is invalid: %w", err)
	}
	if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return os.Rename(partPath, target)
}

func validateXrayArchive(archivePath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), xrayBinaryName()) {
			return nil
		}
	}
	return fmt.Errorf("archive %s does not contain %s", archivePath, xrayBinaryName())
}

func unzipXrayArchive(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if name == "" || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !strings.EqualFold(name, xrayBinaryName()) && !strings.EqualFold(name, "geoip.dat") && !strings.EqualFold(name, "geosite.dat") {
			continue
		}
		targetPath := filepath.Join(destDir, name)
		if err := extractZipFile(f, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(file *zip.File, targetPath string) error {
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(targetPath, 0o755)
	}
	return nil
}

func resolveXrayLogLevel() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XRAY_LOG_LEVEL"))) {
	case "debug", "info", "warning", "error", "none":
		return strings.ToLower(strings.TrimSpace(os.Getenv("XRAY_LOG_LEVEL")))
	default:
		return "error"
	}
}

func buildXrayRuntimeConfig(profiles []xrayResolvedProfile) (string, string, error) {
	if err := os.MkdirAll(xrayWorkingDir(), 0o755); err != nil {
		return "", "", err
	}
	configPath := filepath.Join(xrayWorkingDir(), "config.json")
	logPath := utils.ResolveXrayLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", "", err
	}
	config := map[string]any{
		"log": map[string]any{
			"loglevel": resolveXrayLogLevel(),
		},
		"inbounds":  make([]any, 0, len(profiles)),
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          make([]any, 0, len(profiles)),
		},
	}
	inbounds := config["inbounds"].([]any)
	outbounds := config["outbounds"].([]any)
	rules := config["routing"].(map[string]any)["rules"].([]any)
	for _, item := range profiles {
		profile := item.Profile
		inboundTag := fmt.Sprintf("in-core-%d", profile.ID)
		outboundTag := fmt.Sprintf("out-core-%d", profile.ID)
		// 使用 dokodemo-door 透明代理：Go 直接 TCP 连接本地端口，
		// xray 自动把流量转发到 nvidia API（integrate.api.nvidia.com:443）
		// 不需要 SOCKS5 握手，减少延迟和故障点
		inbounds = append(inbounds, map[string]any{
			"tag":      inboundTag,
			"listen":   item.LocalListenHost,
			"port":     profile.LocalPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{
				"address": "integrate.api.nvidia.com",
				"port":    443,
				"network": "tcp",
			},
		})
		outbound, err := buildXrayOutbound(item, outboundTag)
		if err != nil {
			return "", "", err
		}
		outbounds = append(outbounds, outbound)
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"outboundTag": outboundTag,
		})
	}
	config["inbounds"] = inbounds
	config["outbounds"] = outbounds
	config["routing"].(map[string]any)["rules"] = rules
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return "", "", err
	}
	return configPath, logPath, nil
}

func buildXrayOutbound(item xrayResolvedProfile, tag string) (map[string]any, error) {
	profile := item.Profile
	streamSettings := buildXrayStreamSettings(profile)
	settings := map[string]any{}
	protocol := strings.ToLower(strings.TrimSpace(profile.Protocol))
	switch protocol {
	case "vless":
		settings["vnext"] = []any{map[string]any{
			"address": profile.Server,
			"port":    profile.Port,
			"users": []any{map[string]any{
				"id":         item.PlainAuthID,
				"encryption": "none",
				"flow":       profile.Flow,
			}},
		}}
	case "vmess":
		settings["vnext"] = []any{map[string]any{
			"address": profile.Server,
			"port":    profile.Port,
			"users": []any{map[string]any{
				"id":       item.PlainAuthID,
				"security": "auto",
			}},
		}}
	case "trojan":
		settings["servers"] = []any{map[string]any{
			"address":  profile.Server,
			"port":     profile.Port,
			"password": item.PlainPassword,
		}}
	case "shadowsocks":
		settings["servers"] = []any{map[string]any{
			"address":  profile.Server,
			"port":     profile.Port,
			"method":   profile.Method,
			"password": item.PlainPassword,
		}}
	case "socks":
		server := map[string]any{
			"address": profile.Server,
			"port":    profile.Port,
		}
		if profile.Username != "" || item.PlainPassword != "" {
			server["users"] = []any{map[string]any{
				"user": profile.Username,
				"pass": item.PlainPassword,
			}}
		}
		settings["servers"] = []any{server}
	case "http":
		server := map[string]any{
			"address": profile.Server,
			"port":    profile.Port,
		}
		if profile.Username != "" || item.PlainPassword != "" {
			server["users"] = []any{map[string]any{
				"user": profile.Username,
				"pass": item.PlainPassword,
			}}
		}
		settings["servers"] = []any{server}
	default:
		return nil, fmt.Errorf("unsupported core protocol: %s", profile.Protocol)
	}
	outbound := map[string]any{
		"tag":      tag,
		"protocol": protocol,
		"settings": settings,
	}
	if len(streamSettings) > 0 {
		outbound["streamSettings"] = streamSettings
	}
	return outbound, nil
}

func buildXrayStreamSettings(profile models.CoreProfile) map[string]any {
	profile = models.NormalizeCoreProfile(profile)
	settings := make(map[string]any)
	transport := profile.Transport
	if transport != "" {
		settings["network"] = transport
	}
	switch transport {
	case "ws":
		ws := map[string]any{}
		if profile.Path != "" {
			ws["path"] = profile.Path
		}
		if profile.Host != "" {
			// xray v26+ 要求 host 作为独立字段，不再放在 headers 里
			ws["host"] = profile.Host
		}
		settings["wsSettings"] = ws
	case "grpc":
		settings["grpcSettings"] = map[string]any{"serviceName": profile.ServiceName}
	}
	tlsServerName := strings.TrimSpace(profile.SNI)
	if tlsServerName == "" && net.ParseIP(strings.TrimSpace(profile.Server)) == nil {
		tlsServerName = strings.TrimSpace(profile.Server)
	}
	switch profile.TLSMode {
	case "tls":
		settings["security"] = "tls"
		tlsSettings := map[string]any{
			"allowInsecure": profile.AllowInsecure,
		}
		if tlsServerName != "" {
			tlsSettings["serverName"] = tlsServerName
		}
		settings["tlsSettings"] = tlsSettings
	case "reality":
		settings["security"] = "reality"
		settings["realitySettings"] = map[string]any{
			"serverName":  profile.SNI,
			"publicKey":   profile.RealityPublicKey,
			"shortId":     profile.RealityShortID,
			"fingerprint": firstNonEmpty(profile.Fingerprint, "chrome"),
			"spiderX":     profile.RealitySpiderX,
		}
	}
	return settings
}
