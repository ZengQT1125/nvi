package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/utils"
)

type Store struct {
	APIKeys              []models.APIKey                  `json:"api_keys"`
	Proxies              []models.UpstreamProxy           `json:"proxies,omitempty"`
	MasterKeys           []models.MasterKey               `json:"master_keys"`
	CoreProfiles         []models.CoreProfile             `json:"core_profiles,omitempty"`
	SystemConfig         models.SystemConfig              `json:"system_config"`
	ExternalProxySources models.ExternalProxySources      `json:"external_proxy_sources,omitempty"`
	ProxyImportSchedule  models.ProxyImportSchedule       `json:"proxy_import_schedule,omitempty"`
	ProxyImportLogs      []models.ProxyImportExecutionLog `json:"proxy_import_logs,omitempty"`
	HealthState          json.RawMessage                  `json:"health_state,omitempty"`
	NextAPIID            uint                             `json:"next_api_id"`
	NextProxyID          uint                             `json:"next_proxy_id,omitempty"`
	NextMKID             uint                             `json:"next_master_key_id"`
	NextCoreProfileID    uint                             `json:"next_core_profile_id,omitempty"`
}

type jsonListFile[T any] struct {
	NextID uint `json:"next_id,omitempty"`
	Items  []T  `json:"items"`
}

const (
	apiKeysFilePath             = "keys/api_keys.json"
	masterKeysFilePath          = "keys/master_keys.json"
	proxiesFilePath             = "config/proxies.json"
	coreProfilesFilePath        = "config/core_profiles.json"
	systemConfigFilePath        = "config/system_config.json"
	externalProxySourcesPath    = "config/external_proxy_sources.json"
	proxyImportScheduleFilePath = "config/proxy_import_schedule.json"
	proxyImportLogsFilePath     = "state/proxy_import_logs.json"
	healthStateFilePath         = "state/health_state.json"
	modelCatalogFilePath        = "cache/model_catalog.json"
)

var (
	dataDir        string
	storeDir       string
	legacyStoreSrc string
	// storeMu 仅保护磁盘写路径和 cache 冷启动；读路径直接走 storeCache，几乎无锁。
	storeMu sync.RWMutex
	// storeCache 保存最近一次成功写入或读取的 Store 深拷贝。
	// 鉴权中间件等热路径每次请求都会读 Store，缓存避免了 9 次磁盘 IO + 一把互斥锁。
	storeCache atomic.Pointer[Store]
)

func InitDB(dsn string) {
	if strings.TrimSpace(dsn) == "" {
		dsn = utils.ResolveGatewayStoreDir()
	}
	dataDir, storeDir, legacyStoreSrc = resolveStoreLocation(dsn)

	if err := ensureStoreLayout(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	if _, err := ReadStore(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	log.Printf("Database initialized successfully: %s", storeDir)
}

func resolveStoreLocation(raw string) (string, string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		cleaned := utils.ResolveGatewayStoreDir()
		return filepath.Dir(cleaned), cleaned, ""
	}
	cleaned := filepath.Clean(trimmed)
	if strings.EqualFold(filepath.Ext(cleaned), ".json") {
		parent := filepath.Dir(cleaned)
		dataRoot := filepath.Join(parent, utils.DefaultGatewayDataDirName)
		if strings.EqualFold(filepath.Base(parent), utils.DefaultGatewayDataDirName) {
			dataRoot = parent
		}
		return dataRoot, filepath.Join(dataRoot, utils.DefaultGatewayStoreDirName), cleaned
	}
	if strings.EqualFold(filepath.Base(cleaned), utils.DefaultGatewayDataDirName) {
		return cleaned, filepath.Join(cleaned, utils.DefaultGatewayStoreDirName), ""
	}
	if strings.EqualFold(filepath.Base(cleaned), utils.DefaultGatewayStoreDirName) {
		return filepath.Dir(cleaned), cleaned, ""
	}
	return filepath.Dir(cleaned), cleaned, ""
}

func ensureStoreLayout() error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	if err := handleLegacySQLiteBackup(); err != nil {
		return err
	}
	if err := migrateLegacyDataFiles(); err != nil {
		return err
	}

	hasData, err := hasExistingDataFiles(storeDir)
	if err != nil {
		return err
	}
	if hasData {
		return ensureDefaultFiles()
	}
	for _, candidate := range legacyJSONCandidates() {
		if migrated, err := migrateLegacyJSONStore(candidate); err != nil {
			return err
		} else if migrated {
			return ensureDefaultFiles()
		}
	}
	if err := writeStoreUnlocked(defaultStore()); err != nil {
		return err
	}
	return ensureDefaultFiles()
}

func handleLegacySQLiteBackup() error {
	for _, legacyPath := range legacySQLiteCandidates() {
		if strings.TrimSpace(legacyPath) == "" {
			continue
		}
		if _, err := os.Stat(legacyPath); err == nil {
			backupPath := legacyPath + ".bak"
			if _, backupErr := os.Stat(backupPath); errors.Is(backupErr, os.ErrNotExist) {
				if renameErr := os.Rename(legacyPath, backupPath); renameErr != nil {
					return fmt.Errorf("found legacy sqlite file %s and failed to rename it to %s: %w", legacyPath, backupPath, renameErr)
				}
				log.Printf("Legacy sqlite file %s renamed to %s", legacyPath, backupPath)
			} else if backupErr == nil {
				return fmt.Errorf("found legacy sqlite file %s; remove it because backup %s already exists", legacyPath, backupPath)
			} else {
				return backupErr
			}
		}
	}
	return nil
}

func legacySQLiteCandidates() []string {
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{})
	appendCandidate := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	if legacyStoreSrc != "" && strings.EqualFold(filepath.Base(legacyStoreSrc), "gateway.json") {
		appendCandidate(filepath.Join(filepath.Dir(legacyStoreSrc), "gateway.db"))
	}
	appendCandidate(filepath.Join(dataDir, "gateway.db"))
	appendCandidate(filepath.Join(filepath.Dir(dataDir), "gateway.db"))
	return candidates
}

func hasExistingDataFiles(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if errors.Is(err, filepath.SkipAll) {
			return found, nil
		}
		return false, err
	}
	return found, nil
}

func migrateLegacyDataFiles() error {
	legacyPaths := map[string]string{
		"api_keys.json":                  apiKeysFilePath,
		"master_keys.json":               masterKeysFilePath,
		"proxies.json":                   proxiesFilePath,
		"core_profiles.json":             coreProfilesFilePath,
		"system_config.json":             systemConfigFilePath,
		"external_proxy_sources.json":    externalProxySourcesPath,
		"proxy_import_schedule.json":     proxyImportScheduleFilePath,
		"proxy_import_logs.json":         proxyImportLogsFilePath,
		"health_state.json":              healthStateFilePath,
		"model_catalog.json":             modelCatalogFilePath,
		apiKeysFilePath:                  apiKeysFilePath,
		masterKeysFilePath:               masterKeysFilePath,
		proxiesFilePath:                  proxiesFilePath,
		coreProfilesFilePath:             coreProfilesFilePath,
		systemConfigFilePath:             systemConfigFilePath,
		externalProxySourcesPath:         externalProxySourcesPath,
		proxyImportScheduleFilePath:      proxyImportScheduleFilePath,
		"runtime/proxy_import_logs.json": proxyImportLogsFilePath,
		"runtime/health_state.json":      healthStateFilePath,
		"runtime/model_catalog.json":     modelCatalogFilePath,
		proxyImportLogsFilePath:          proxyImportLogsFilePath,
		healthStateFilePath:              healthStateFilePath,
		modelCatalogFilePath:             modelCatalogFilePath,
	}
	for _, sourceRoot := range legacyDataRoots() {
		for sourceRelative, targetRelative := range legacyPaths {
			sourcePath := filepath.Join(sourceRoot, filepath.FromSlash(sourceRelative))
			targetPath := filepath.Join(storeDir, filepath.FromSlash(targetRelative))
			if strings.EqualFold(filepath.Clean(sourcePath), filepath.Clean(targetPath)) {
				continue
			}
			if _, err := os.Stat(sourcePath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			if _, err := os.Stat(targetPath); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.Rename(sourcePath, targetPath); err != nil {
				return err
			}
			log.Printf("Migrated legacy data file %s to %s", sourcePath, targetPath)
		}
	}
	return nil
}

func legacyDataRoots() []string {
	roots := make([]string, 0, 2)
	seen := make(map[string]struct{})
	appendRoot := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		roots = append(roots, cleaned)
	}
	appendRoot(storeDir)
	appendRoot(dataDir)
	return roots
}

func legacyJSONCandidates() []string {
	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{})
	appendCandidate := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	appendCandidate(legacyStoreSrc)
	appendCandidate(filepath.Join(storeDir, "gateway.json"))
	appendCandidate(filepath.Join(dataDir, "gateway.json"))
	appendCandidate(filepath.Join(filepath.Dir(dataDir), "gateway.json"))
	return candidates
}

func migrateLegacyJSONStore(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return false, fmt.Errorf("%s is not a valid JSON store file: %w", path, err)
	}
	if err := writeStoreUnlocked(normalizeStore(&store)); err != nil {
		return false, err
	}
	if err := backupMigratedLegacyFile(path); err != nil {
		return false, err
	}
	log.Printf("Migrated legacy JSON store %s into %s", path, storeDir)
	return true, nil
}

func backupMigratedLegacyFile(path string) error {
	base := path + ".migrated.bak"
	candidate := base
	for idx := 1; ; idx++ {
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return os.Rename(path, candidate)
		} else if err != nil {
			return err
		}
		candidate = fmt.Sprintf("%s.%d", base, idx)
	}
}

func ensureDefaultFiles() error {
	defaults := defaultStore()
	if err := ensureJSONFile(filepath.Join(storeDir, apiKeysFilePath), jsonListFile[models.APIKey]{NextID: defaults.NextAPIID, Items: defaults.APIKeys}); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, proxiesFilePath), jsonListFile[models.UpstreamProxy]{NextID: defaults.NextProxyID, Items: defaults.Proxies}); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, masterKeysFilePath), jsonListFile[models.MasterKey]{NextID: defaults.NextMKID, Items: defaults.MasterKeys}); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, coreProfilesFilePath), jsonListFile[models.CoreProfile]{NextID: defaults.NextCoreProfileID, Items: defaults.CoreProfiles}); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, systemConfigFilePath), defaults.SystemConfig); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, externalProxySourcesPath), defaults.ExternalProxySources); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, proxyImportScheduleFilePath), defaults.ProxyImportSchedule); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, proxyImportLogsFilePath), defaults.ProxyImportLogs); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, healthStateFilePath), map[string]any{}); err != nil {
		return err
	}
	if err := ensureJSONFile(filepath.Join(storeDir, modelCatalogFilePath), make([]any, 0)); err != nil {
		return err
	}
	return nil
}

func ensureJSONFile(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSONFile(path, value)
}

// ReadStore 返回当前 Store 的深拷贝。
// 调用方可以自由修改返回值，不会污染缓存。
// 热路径上会命中 storeCache，避免每次都做 9 次磁盘 IO。
func ReadStore() (*Store, error) {
	if cached := storeCache.Load(); cached != nil {
		return cloneStore(cached), nil
	}
	// 冷启动 / 缓存被清理：拿写锁去读磁盘并回填缓存。
	storeMu.Lock()
	defer storeMu.Unlock()
	// 双重检查：可能在等锁期间已有别的 goroutine 把缓存写好了。
	if cached := storeCache.Load(); cached != nil {
		return cloneStore(cached), nil
	}
	store, err := readStoreUnlocked()
	if err != nil {
		return nil, err
	}
	storeCache.Store(cloneStore(store))
	return store, nil
}

func UpdateStore(mutator func(*Store) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()

	// 优先用缓存里的最近一次快照避免再读磁盘；只在缓存为空时才回退到磁盘。
	var store *Store
	if cached := storeCache.Load(); cached != nil {
		store = cloneStore(cached)
	} else {
		loaded, err := readStoreUnlocked()
		if err != nil {
			return err
		}
		store = loaded
	}
	if err := mutator(store); err != nil {
		return err
	}
	// writeStoreUnlocked 会在写磁盘成功后刷新 storeCache。
	return writeStoreUnlocked(normalizeStore(store))
}

// cloneStore 通过 JSON round-trip 做一次深拷贝。
// 用 JSON 是为了把内嵌的 slice、time.Time、json.RawMessage 等都正确复制，
// 避免出现"缓存被外部 mutator 改坏"的情况。
func cloneStore(src *Store) *Store {
	if src == nil {
		return nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		// 极端情况下退化为浅拷贝，保证可用性优先。
		shallow := *src
		return &shallow
	}
	var dst Store
	if err := json.Unmarshal(data, &dst); err != nil {
		shallow := *src
		return &shallow
	}
	return normalizeStore(&dst)
}

func readStoreUnlocked() (*Store, error) {
	store := defaultStore()

	apiKeysFile := jsonListFile[models.APIKey]{NextID: store.NextAPIID, Items: store.APIKeys}
	if err := readJSONFile(filepath.Join(storeDir, apiKeysFilePath), &apiKeysFile); err != nil {
		return nil, err
	}
	store.APIKeys = apiKeysFile.Items
	store.NextAPIID = apiKeysFile.NextID

	proxiesFile := jsonListFile[models.UpstreamProxy]{NextID: store.NextProxyID, Items: store.Proxies}
	if err := readJSONFile(filepath.Join(storeDir, proxiesFilePath), &proxiesFile); err != nil {
		return nil, err
	}
	store.Proxies = proxiesFile.Items
	store.NextProxyID = proxiesFile.NextID

	masterKeysFile := jsonListFile[models.MasterKey]{NextID: store.NextMKID, Items: store.MasterKeys}
	if err := readJSONFile(filepath.Join(storeDir, masterKeysFilePath), &masterKeysFile); err != nil {
		return nil, err
	}
	store.MasterKeys = masterKeysFile.Items
	store.NextMKID = masterKeysFile.NextID

	coreProfilesFile := jsonListFile[models.CoreProfile]{NextID: store.NextCoreProfileID, Items: store.CoreProfiles}
	if err := readJSONFile(filepath.Join(storeDir, coreProfilesFilePath), &coreProfilesFile); err != nil {
		return nil, err
	}
	store.CoreProfiles = coreProfilesFile.Items
	store.NextCoreProfileID = coreProfilesFile.NextID

	if err := readJSONFile(filepath.Join(storeDir, systemConfigFilePath), &store.SystemConfig); err != nil {
		return nil, err
	}
	if err := readJSONFile(filepath.Join(storeDir, externalProxySourcesPath), &store.ExternalProxySources); err != nil {
		return nil, err
	}
	if err := readJSONFile(filepath.Join(storeDir, proxyImportScheduleFilePath), &store.ProxyImportSchedule); err != nil {
		return nil, err
	}
	if err := readJSONFile(filepath.Join(storeDir, proxyImportLogsFilePath), &store.ProxyImportLogs); err != nil {
		return nil, err
	}

	healthStateRaw, err := readRawJSONFile(filepath.Join(storeDir, healthStateFilePath))
	if err != nil {
		return nil, err
	}
	modelCatalogRaw, err := readRawJSONFile(filepath.Join(storeDir, modelCatalogFilePath))
	if err != nil {
		return nil, err
	}
	mergedHealthState, err := mergeHealthState(healthStateRaw, modelCatalogRaw)
	if err != nil {
		return nil, err
	}
	store.HealthState = mergedHealthState

	return normalizeStore(store), nil
}

func writeStore(store *Store) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	return writeStoreUnlocked(normalizeStore(store))
}

func writeStoreUnlocked(store *Store) error {
	store = normalizeStore(store)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, apiKeysFilePath), jsonListFile[models.APIKey]{NextID: store.NextAPIID, Items: store.APIKeys}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, proxiesFilePath), jsonListFile[models.UpstreamProxy]{NextID: store.NextProxyID, Items: store.Proxies}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, masterKeysFilePath), jsonListFile[models.MasterKey]{NextID: store.NextMKID, Items: store.MasterKeys}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, coreProfilesFilePath), jsonListFile[models.CoreProfile]{NextID: store.NextCoreProfileID, Items: store.CoreProfiles}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, systemConfigFilePath), store.SystemConfig); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, externalProxySourcesPath), store.ExternalProxySources); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, proxyImportScheduleFilePath), store.ProxyImportSchedule); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(storeDir, proxyImportLogsFilePath), store.ProxyImportLogs); err != nil {
		return err
	}
	healthStateRaw, modelCatalogRaw, err := splitHealthState(store.HealthState)
	if err != nil {
		return err
	}
	if err := writeRawJSONFile(filepath.Join(storeDir, healthStateFilePath), healthStateRaw, []byte("{}\n")); err != nil {
		return err
	}
	if err := writeRawJSONFile(filepath.Join(storeDir, modelCatalogFilePath), modelCatalogRaw, []byte("[]\n")); err != nil {
		return err
	}
	// 写磁盘成功后才更新缓存，保证缓存只反映已持久化的状态。
	// 这里再拷贝一份，避免后续 caller 修改 store 引用污染缓存。
	storeCache.Store(cloneStore(store))
	return nil
}

// isAllNUL 判定一个文件内容是否完全由 NUL 字节构成。
// 这通常发生在 Windows / macOS 上：进程在 os.WriteFile 之后、fsync 之前崩溃或被强杀，
// NTFS / APFS 把 metadata（含文件长度）刷到了磁盘，但数据本身还在 OS cache 里没刷。
// 重启之后该文件以"已分配长度但全是零字节"出现，json.Unmarshal 会得到经典的
//   `invalid character '\x00' looking for beginning of value`。
// 把这种情况识别成"等同于不存在"是合理的：原始数据已经无法挽回，
// 启动时退化为默认值好过整个进程起不来。
func isAllNUL(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if isAllNUL(data) {
		log.Printf("WARN: %s 内容全为 NUL（%d 字节），疑似上一次写入未 fsync 即崩溃；按空文件处理，将退化为默认值", path, len(data))
		return nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return json.Unmarshal(data, out)
}

func readRawJSONFile(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if isAllNUL(data) {
		log.Printf("WARN: %s 内容全为 NUL（%d 字节），按空文件处理", path, len(data))
		return nil, nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	return json.RawMessage(data), nil
}

func splitHealthState(raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil, nil
	}
	state := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, nil, err
	}
	modelCatalog := state["modelCatalog"]
	delete(state, "modelCatalog")
	if len(state) == 0 {
		return nil, modelCatalog, nil
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	encoded = append(encoded, '\n')
	return encoded, modelCatalog, nil
}

func mergeHealthState(stateRaw, modelCatalogRaw json.RawMessage) (json.RawMessage, error) {
	state := make(map[string]json.RawMessage)
	if len(strings.TrimSpace(string(stateRaw))) > 0 {
		if err := json.Unmarshal(stateRaw, &state); err != nil {
			return nil, err
		}
	}
	if len(strings.TrimSpace(string(modelCatalogRaw))) > 0 {
		state["modelCatalog"] = modelCatalogRaw
	}
	if len(state) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeDataFile(path, data)
}

func writeRawJSONFile(path string, raw json.RawMessage, fallback []byte) error {
	data := raw
	if len(strings.TrimSpace(string(data))) == 0 {
		data = fallback
	}
	return writeDataFile(path, data)
}

// writeDataFile 用 "写 .tmp + fsync + rename" 模式做 crash-safe 写入。
// 关键是写完数据后必须 f.Sync()：否则 OS 可能先把 metadata（文件大小）刷到磁盘，
// 数据还留在 page cache 里。一旦此时崩溃 / 断电，重启后该文件就会以"已分配大小但
// 内容全是 NUL"出现，进程下次启动 json.Unmarshal 时直接报
// `invalid character '\x00' looking for beginning of value`，从而无法启动。
func writeDataFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	cleanup := func(closeErr error) error {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return cleanup(err)
	}
	// fsync 强制把数据从 page cache 刷到磁盘 platter / SSD cell。
	// 没有这一步，下面 Rename 让文件被"看到"时数据可能还没落盘。
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(tmpPath, path); err == nil {
		return nil
	}
	// Windows 下 Rename 可能因为目标文件被占用而失败，先 Remove 再 Rename 兜底。
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return os.Rename(tmpPath, path)
}

func defaultStore() *Store {
	return &Store{
		APIKeys:              make([]models.APIKey, 0),
		Proxies:              make([]models.UpstreamProxy, 0),
		MasterKeys:           make([]models.MasterKey, 0),
		CoreProfiles:         make([]models.CoreProfile, 0),
		SystemConfig:         models.DefaultSystemConfig(),
		ExternalProxySources: models.DefaultExternalProxySources(),
		ProxyImportSchedule:  models.DefaultProxyImportSchedule(),
		ProxyImportLogs:      make([]models.ProxyImportExecutionLog, 0),
		NextAPIID:            1,
		NextProxyID:          1,
		NextMKID:             1,
		NextCoreProfileID:    1,
	}
}

func normalizeStore(store *Store) *Store {
	if store == nil {
		return defaultStore()
	}
	if store.APIKeys == nil {
		store.APIKeys = make([]models.APIKey, 0)
	}
	if store.Proxies == nil {
		store.Proxies = make([]models.UpstreamProxy, 0)
	}
	if store.MasterKeys == nil {
		store.MasterKeys = make([]models.MasterKey, 0)
	}
	if store.CoreProfiles == nil {
		store.CoreProfiles = make([]models.CoreProfile, 0)
	}
	if store.ProxyImportLogs == nil {
		store.ProxyImportLogs = make([]models.ProxyImportExecutionLog, 0)
	}
	for i := range store.Proxies {
		store.Proxies[i] = models.NormalizeUpstreamProxy(store.Proxies[i])
		if store.Proxies[i].TestHistory == nil {
			store.Proxies[i].TestHistory = make([]models.ProxyTestRecord, 0)
		}
	}
	for i := range store.CoreProfiles {
		store.CoreProfiles[i] = models.NormalizeCoreProfile(store.CoreProfiles[i])
	}
	store.SystemConfig = models.NormalizeSystemConfig(store.SystemConfig)
	store.ExternalProxySources = models.NormalizeExternalProxySources(store.ExternalProxySources)
	store.ProxyImportSchedule = models.NormalizeProxyImportSchedule(store.ProxyImportSchedule)
	if store.NextAPIID == 0 {
		store.NextAPIID = nextAPIID(store.APIKeys)
	}
	if store.NextProxyID == 0 {
		store.NextProxyID = nextProxyID(store.Proxies)
	}
	if store.NextMKID == 0 {
		store.NextMKID = nextMasterKeyID(store.MasterKeys)
	}
	if store.NextCoreProfileID == 0 {
		store.NextCoreProfileID = nextCoreProfileID(store.CoreProfiles)
	}
	return store
}

func nextAPIID(keys []models.APIKey) uint {
	var maxID uint
	for _, key := range keys {
		if key.ID > maxID {
			maxID = key.ID
		}
	}
	return maxID + 1
}

func nextProxyID(proxies []models.UpstreamProxy) uint {
	var maxID uint
	for _, proxy := range proxies {
		if proxy.ID > maxID {
			maxID = proxy.ID
		}
	}
	return maxID + 1
}

func nextMasterKeyID(keys []models.MasterKey) uint {
	var maxID uint
	for _, key := range keys {
		if key.ID > maxID {
			maxID = key.ID
		}
	}
	return maxID + 1
}

func nextCoreProfileID(items []models.CoreProfile) uint {
	var maxID uint
	for _, item := range items {
		if item.ID > maxID {
			maxID = item.ID
		}
	}
	return maxID + 1
}
