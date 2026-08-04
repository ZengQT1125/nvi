package utils

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultGatewayDataDirName  = "data"
	DefaultGatewayStoreDirName = "store"
	DefaultGatewayLogsDirName  = "logs"
)

func ResolveProjectRoot() string {
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Clean(cwd)
	}
	if exePath, err := os.Executable(); err == nil {
		return filepath.Clean(filepath.Dir(exePath))
	}
	return "."
}

func ResolveAbsolutePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if absolute, err := filepath.Abs(trimmed); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(trimmed)
}

func ResolveGatewayDataDir() string {
	if custom := strings.TrimSpace(os.Getenv("GATEWAY_DATA_DIR")); custom != "" {
		return ResolveAbsolutePath(custom)
	}
	if custom := strings.TrimSpace(os.Getenv("GATEWAY_STORE_PATH")); custom != "" {
		cleaned := ResolveAbsolutePath(custom)
		if strings.EqualFold(filepath.Ext(cleaned), ".json") {
			return filepath.Join(filepath.Dir(cleaned), DefaultGatewayDataDirName)
		}
		switch strings.ToLower(strings.TrimSpace(filepath.Base(cleaned))) {
		case DefaultGatewayStoreDirName:
			return filepath.Dir(cleaned)
		case DefaultGatewayDataDirName:
			return cleaned
		default:
			return cleaned
		}
	}
	return filepath.Join(ResolveProjectRoot(), DefaultGatewayDataDirName)
}

func ResolveGatewayStoreDir() string {
	return filepath.Join(ResolveGatewayDataDir(), DefaultGatewayStoreDirName)
}

func ResolveGatewayLogsDir() string {
	return filepath.Join(ResolveGatewayDataDir(), DefaultGatewayLogsDirName)
}

func ResolveBackendLogPath() string {
	return filepath.Join(ResolveGatewayLogsDir(), "backend", "backend.log")
}

func ResolveFrontendLogPath() string {
	return filepath.Join(ResolveGatewayLogsDir(), "frontend", "frontend.log")
}

func ResolveXrayCoreDir() string {
	if custom := strings.TrimSpace(os.Getenv("XRAY_CORE_DIR")); custom != "" {
		return ResolveAbsolutePath(custom)
	}
	return filepath.Join(ResolveProjectRoot(), "bin", "xray")
}

func ResolveXrayLogPath() string {
	return filepath.Join(ResolveGatewayLogsDir(), "xray", "xray.log")
}
