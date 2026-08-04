package models

import (
	"sort"
	"strings"
	"time"
)

const (
	CoreProfileStatusEnabled  = "Enabled"
	CoreProfileStatusDisabled = "Disabled"
	CoreManagedByXray         = "xray"
	CoreLocalHost             = "127.0.0.1"
	CoreLocalPortStart        = 21001
)

var supportedShadowsocksMethods = map[string]struct{}{
	"2022-blake3-aes-128-gcm":       {},
	"2022-blake3-aes-256-gcm":       {},
	"2022-blake3-chacha20-poly1305": {},
	"aes-128-gcm":                   {},
	"aes-256-gcm":                   {},
	"chacha20-poly1305":             {},
	"chacha20-ietf-poly1305":        {},
	"xchacha20-poly1305":            {},
	"xchacha20-ietf-poly1305":       {},
	"none":                          {},
	"plain":                         {},
}

type CoreProfile struct {
	ID               uint             `json:"id"`
	Name             string           `json:"name"`
	Protocol         string           `json:"protocol"`
	Status           string           `json:"status"`
	Server           string           `json:"server"`
	Port             int              `json:"port"`
	LocalPort        int              `json:"local_port"`
	Transport        string           `json:"transport,omitempty"`
	TLSMode          string           `json:"tls_mode,omitempty"`
	SNI              string           `json:"sni,omitempty"`
	AllowInsecure    bool             `json:"allow_insecure,omitempty"`
	Host             string           `json:"host,omitempty"`
	Path             string           `json:"path,omitempty"`
	ServiceName      string           `json:"service_name,omitempty"`
	Flow             string           `json:"flow,omitempty"`
	Method           string           `json:"method,omitempty"`
	Username         string           `json:"username,omitempty"`
	Password         string           `json:"password,omitempty"`
	AuthID           string           `json:"auth_id,omitempty"`
	Fingerprint      string           `json:"fingerprint,omitempty"`
	RealityPublicKey string           `json:"reality_public_key,omitempty"`
	RealityShortID   string           `json:"reality_short_id,omitempty"`
	RealitySpiderX   string           `json:"reality_spider_x,omitempty"`
	ManagedProxyID   uint             `json:"managed_proxy_id,omitempty"`
	Remarks          string           `json:"remarks,omitempty"`
	LastTest         *ProxyTestRecord `json:"last_test,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

func NormalizeCoreProfile(profile CoreProfile) CoreProfile {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Protocol = strings.ToLower(strings.TrimSpace(profile.Protocol))
	profile.Status = normalizeCoreProfileStatus(profile.Status)
	profile.Server = strings.TrimSpace(profile.Server)
	profile.Transport = normalizeCoreProfileTransport(profile.Transport)
	profile.TLSMode = normalizeCoreProfileTLSMode(profile.Protocol, profile.TLSMode)
	profile.SNI = strings.TrimSpace(profile.SNI)
	profile.Host = strings.TrimSpace(profile.Host)
	profile.Path = strings.TrimSpace(profile.Path)
	profile.ServiceName = strings.TrimSpace(profile.ServiceName)
	profile.Flow = strings.TrimSpace(profile.Flow)
	profile.Method = normalizeCoreProfileMethod(profile.Protocol, profile.Method)
	profile.Username = strings.TrimSpace(profile.Username)
	profile.Password = strings.TrimSpace(profile.Password)
	profile.AuthID = strings.TrimSpace(profile.AuthID)
	profile.Fingerprint = strings.TrimSpace(profile.Fingerprint)
	profile.RealityPublicKey = strings.TrimSpace(profile.RealityPublicKey)
	profile.RealityShortID = strings.TrimSpace(profile.RealityShortID)
	profile.RealitySpiderX = strings.TrimSpace(profile.RealitySpiderX)
	profile.Remarks = strings.TrimSpace(profile.Remarks)
	if profile.LocalPort < 0 {
		profile.LocalPort = 0
	}
	return profile
}

func normalizeCoreProfileStatus(status string) string {
	switch strings.TrimSpace(status) {
	case CoreProfileStatusDisabled:
		return CoreProfileStatusDisabled
	default:
		return CoreProfileStatusEnabled
	}
}

func normalizeCoreProfileTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ws":
		return "ws"
	case "grpc":
		return "grpc"
	default:
		return "tcp"
	}
}

func normalizeCoreProfileTLSMode(protocol string, value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tls":
		return "tls"
	case "reality":
		return "reality"
	}
	if strings.ToLower(strings.TrimSpace(protocol)) == "trojan" {
		return "tls"
	}
	return "none"
}

func normalizeCoreProfileMethod(protocol string, method string) string {
	trimmed := strings.ToLower(strings.TrimSpace(method))
	if strings.ToLower(strings.TrimSpace(protocol)) != "shadowsocks" {
		return trimmed
	}
	if trimmed == "plain" {
		return "none"
	}
	return trimmed
}

func IsSupportedShadowsocksMethod(method string) bool {
	_, ok := supportedShadowsocksMethods[normalizeCoreProfileMethod("shadowsocks", method)]
	return ok
}

func SupportedShadowsocksMethods() []string {
	items := make([]string, 0, len(supportedShadowsocksMethods))
	for method := range supportedShadowsocksMethods {
		if method == "plain" {
			continue
		}
		items = append(items, method)
	}
	sort.Strings(items)
	return items
}
