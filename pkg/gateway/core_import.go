package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/models"

	"github.com/gofiber/fiber/v2"
)

type importCoreProfilesRequest struct {
	SubscriptionURL string `json:"subscriptionUrl,omitempty"`
	RawText         string `json:"rawText,omitempty"`
}

type importCoreProfilesResponse struct {
	Message      string                `json:"message"`
	Imported     int                   `json:"imported"`
	Skipped      int                   `json:"skipped"`
	Profiles     []coreProfileResponse `json:"profiles,omitempty"`
	Warnings     []string              `json:"warnings,omitempty"`
	Subscription string                `json:"subscription,omitempty"`
	Runtime      xrayRuntimeSnapshot   `json:"runtime"`
}

type xrayRuntimeLogsResponse struct {
	Path      string `json:"path,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Content   string `json:"content"`
}

var shareLinkPattern = regexp.MustCompile(`(?i)(vmess|vless|trojan|ss|socks)://[^\s"'<>]+`)

// shareLinkSplitPattern 用于在拼接文本中按代理协议头切割多条链接。
// 只匹配代理协议（vmess/vless/trojan/ss/socks），不包含 http/https，
// 因为 http/https 可能出现在节点名称（fragment）里作为推广链接，不应作为切割点。
var shareLinkSplitPattern = regexp.MustCompile(`(?i)(?:vmess|vless|trojan|ss|socks5?)://`)

func ImportCoreProfiles(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req importCoreProfilesRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "请求体格式无效"})
		}
		rawInput := strings.TrimSpace(req.RawText)
		if strings.TrimSpace(req.SubscriptionURL) == "" && rawInput == "" {
			return c.Status(400).JSON(fiber.Map{"error": "请填写订阅 URL 或粘贴分享链接 / 订阅内容"})
		}
		warnings := make([]string, 0)
		if strings.TrimSpace(req.SubscriptionURL) != "" {
			text, err := fetchSubscriptionText(c.Context(), strings.TrimSpace(req.SubscriptionURL))
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "获取订阅内容失败: " + err.Error()})
			}
			if rawInput != "" {
				rawInput += "\n"
			}
			rawInput += text
		}
		parsedRequests, parseWarnings, err := parseCoreImportRequests(rawInput)
		warnings = append(warnings, parseWarnings...)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if len(parsedRequests) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "没有解析到可导入的节点链接"})
		}
		imported := make([]models.CoreProfile, 0)
		skipped := 0
		err = db.UpdateStore(func(store *db.Store) error {
			store.CoreProfiles = normalizeAndAllocateCoreProfiles(store.CoreProfiles)
			existing := make(map[string]struct{}, len(store.CoreProfiles))
			for _, item := range store.CoreProfiles {
				plainAuthID, _ := decryptCoreSecret(item.AuthID)
				plainPassword, _ := decryptCoreSecret(item.Password)
				existing[coreProfileFingerprint(item, plainAuthID, plainPassword)] = struct{}{}
			}
			for _, reqItem := range parsedRequests {
				fingerprint := coreProfileRequestFingerprint(reqItem)
				if _, ok := existing[fingerprint]; ok {
					skipped++
					continue
				}
				profile, buildErr := buildCoreProfileFromCreate(reqItem)
				if buildErr != nil {
					warnings = append(warnings, fmt.Sprintf("跳过节点 %s: %s", firstNonEmpty(reqItem.Name, reqItem.Server), buildErr.Error()))
					skipped++
					continue
				}
				profile.ID = store.NextCoreProfileID
				store.NextCoreProfileID++
				store.CoreProfiles = append(store.CoreProfiles, profile)
				imported = append(imported, profile)
				existing[fingerprint] = struct{}{}
			}
			return nil
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "导入核心节点失败: " + err.Error()})
		}
		if err := manager.Reload(context.Background()); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "节点已导入，但重载 Xray 失败: " + err.Error()})
		}
		responses := make([]coreProfileResponse, 0, len(imported))
		for _, item := range imported {
			responses = append(responses, newCoreProfileResponse(item))
		}
		return c.JSON(importCoreProfilesResponse{
			Message:      fmt.Sprintf("导入完成：新增 %d 个，跳过 %d 个。", len(imported), skipped),
			Imported:     len(imported),
			Skipped:      skipped,
			Profiles:     responses,
			Warnings:     warnings,
			Subscription: strings.TrimSpace(req.SubscriptionURL),
			Runtime:      manager.Snapshot(),
		})
	}
}

func GetCoreRuntimeLogs(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		lines, _ := strconv.Atoi(strings.TrimSpace(c.Query("lines", "200")))
		if lines <= 0 {
			lines = 200
		}
		if lines > 2000 {
			lines = 2000
		}
		runtimeSnapshot := manager.Snapshot()
		content := ""
		if strings.TrimSpace(runtimeSnapshot.LogPath) != "" {
			if data, err := tailFileLines(runtimeSnapshot.LogPath, lines); err == nil {
				content = data
			}
		}
		return c.JSON(xrayRuntimeLogsResponse{
			Path:      runtimeSnapshot.LogPath,
			LastError: runtimeSnapshot.LastError,
			Content:   content,
		})
	}
}

func ClearCoreRuntimeLogs(manager *XrayCoreManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path, err := manager.ClearLogs()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "清空 Xray 日志失败: " + err.Error()})
		}
		return c.JSON(fiber.Map{
			"message": "Xray 日志已清空",
			"path":    path,
			"runtime": manager.Snapshot(),
		})
	}
}

func fetchSubscriptionText(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("订阅地址只支持 http / https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "nvidia-api-gateway")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("http %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return normalizeSubscriptionPayload(string(body)), nil
}

func normalizeSubscriptionPayload(raw string) string {
	return normalizeSubscriptionPayloadDepth(raw, 0)
}

func normalizeSubscriptionPayloadDepth(raw string, depth int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if depth >= 3 {
		return html.UnescapeString(trimmed)
	}
	candidates := []string{trimmed, html.UnescapeString(trimmed), decodePercentLoose(trimmed)}
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		if shareLinkPattern.MatchString(item) {
			return item
		}
		if decoded, err := decodeBase64Any(compactBase64(item)); err == nil {
			decodedText := strings.TrimSpace(string(decoded))
			if decodedText != "" && decodedText != item {
				normalized := normalizeSubscriptionPayloadDepth(decodedText, depth+1)
				if shareLinkPattern.MatchString(normalized) {
					return normalized
				}
			}
		}
	}
	return html.UnescapeString(trimmed)
}

func parseCoreImportRequests(raw string) ([]createCoreProfileRequest, []string, error) {
	text := normalizeSubscriptionPayload(raw)
	links := extractShareLinks(text)
	if len(links) == 0 {
		return nil, nil, errors.New("未发现 vmess/vless/trojan/ss/socks/http 分享链接")
	}
	warnings := make([]string, 0)
	items := make([]createCoreProfileRequest, 0, len(links))
	seen := make(map[string]struct{})
	for _, link := range links {
		req, err := parseCoreShareLink(link)
		if err != nil {
			// skip:xxx 表示静默跳过（如 http/https 推广链接），不计入警告
			if strings.HasPrefix(err.Error(), "skip:") {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("解析失败: %s", err.Error()))
			continue
		}
		fp := coreProfileRequestFingerprint(req)
		if _, ok := seen[fp]; ok {
			continue
		}
		seen[fp] = struct{}{}
		items = append(items, req)
	}
	return items, warnings, nil
}

func extractShareLinks(text string) []string {
	// 对原始文本、HTML 反转义、URL 解码三种形式分别提取
	// 不再对整段文本做 normalizeSubscriptionPayload，避免对已是明文链接的输入产生损坏的副本
	candidates := []string{text, html.UnescapeString(text), decodePercentLoose(text)}
	items := make([]string, 0)
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		matches := shareLinkPattern.FindAllString(candidate, -1)
		for _, raw := range matches {
			// 一条正则匹配结果里可能包含多条拼接的链接（无换行分隔），
			// 例如 "...#香港 8vless://..." 或 "...==vmess://..."
			// 用协议头作为分割点拆开，再逐条处理
			for _, item := range splitByProtocolBoundary(raw) {
				trimmed := strings.TrimSpace(strings.TrimRight(html.UnescapeString(item), ",;"))
				if trimmed == "" {
					continue
				}
				if _, ok := seen[trimmed]; ok {
					continue
				}
				seen[trimmed] = struct{}{}
				items = append(items, trimmed)
			}
		}
	}
	return items
}

// splitByProtocolBoundary 将一段可能拼接了多条链接的字符串按协议头切割成独立链接。
// 例如 "vmess://AAA==vless://BBB" → ["vmess://AAA==", "vless://BBB"]
func splitByProtocolBoundary(raw string) []string {
	locs := shareLinkSplitPattern.FindAllStringIndex(raw, -1)
	if len(locs) <= 1 {
		return []string{raw}
	}
	parts := make([]string, 0, len(locs))
	for i, loc := range locs {
		start := loc[0]
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(raw)
		}
		part := strings.TrimSpace(raw[start:end])
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func parseCoreShareLink(link string) (createCoreProfileRequest, error) {
	switch {
	case strings.HasPrefix(strings.ToLower(link), "vmess://"):
		return parseVMessShareLink(link)
	case strings.HasPrefix(strings.ToLower(link), "vless://"):
		return parseVLESSShareLink(link)
	case strings.HasPrefix(strings.ToLower(link), "trojan://"):
		return parseTrojanShareLink(link)
	case strings.HasPrefix(strings.ToLower(link), "ss://"):
		return parseShadowsocksShareLink(link)
	case strings.HasPrefix(strings.ToLower(link), "socks://"):
		return parseSimpleProxyShareLink(link, "socks")
	case strings.HasPrefix(strings.ToLower(link), "http://"):
		// http:// 可能是节点名称（fragment）里的推广链接，静默跳过
		return createCoreProfileRequest{}, fmt.Errorf("skip:http")
	case strings.HasPrefix(strings.ToLower(link), "https://"):
		// https:// 同上，静默跳过
		return createCoreProfileRequest{}, fmt.Errorf("skip:https")
	default:
		return createCoreProfileRequest{}, fmt.Errorf("不支持的分享链接: %s", link)
	}
}

func parseVLESSShareLink(link string) (createCoreProfileRequest, error) {
	u, err := url.Parse(link)
	if err != nil {
		return createCoreProfileRequest{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return createCoreProfileRequest{}, errors.New("VLESS 端口无效")
	}
	q := u.Query()
	return createCoreProfileRequest{
		Name:             decodeFragment(u.Fragment),
		Protocol:         "vless",
		Status:           models.CoreProfileStatusEnabled,
		Server:           u.Hostname(),
		Port:             port,
		Transport:        firstNonEmpty(q.Get("type"), q.Get("network"), "tcp"),
		TLSMode:          normalizeShareSecurityMode(q.Get("security")),
		SNI:              firstNonEmpty(q.Get("sni"), q.Get("serverName")),
		Host:             q.Get("host"),
		Path:             decodePercent(q.Get("path")),
		ServiceName:      q.Get("serviceName"),
		Flow:             q.Get("flow"),
		AuthID:           u.User.Username(),
		Fingerprint:      q.Get("fp"),
		RealityPublicKey: q.Get("pbk"),
		RealityShortID:   q.Get("sid"),
		RealitySpiderX:   q.Get("spx"),
		AllowInsecure:    parseBoolLoose(q.Get("allowInsecure")) || parseBoolLoose(q.Get("insecure")),
	}, nil
}

func parseTrojanShareLink(link string) (createCoreProfileRequest, error) {
	u, err := url.Parse(link)
	if err != nil {
		return createCoreProfileRequest{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return createCoreProfileRequest{}, errors.New("Trojan 端口无效")
	}
	password, _ := u.User.Password()
	if password == "" {
		password = u.User.Username()
	}
	q := u.Query()
	return createCoreProfileRequest{
		Name:          decodeFragment(u.Fragment),
		Protocol:      "trojan",
		Status:        models.CoreProfileStatusEnabled,
		Server:        u.Hostname(),
		Port:          port,
		Transport:     firstNonEmpty(q.Get("type"), q.Get("network"), "tcp"),
		TLSMode:       normalizeShareSecurityMode(q.Get("security")),
		SNI:           firstNonEmpty(q.Get("sni"), q.Get("serverName")),
		Host:          q.Get("host"),
		Path:          decodePercent(q.Get("path")),
		ServiceName:   q.Get("serviceName"),
		Password:      password,
		Fingerprint:   q.Get("fp"),
		AllowInsecure: parseBoolLoose(q.Get("allowInsecure")) || parseBoolLoose(q.Get("insecure")),
	}, nil
}

func parseShadowsocksShareLink(link string) (createCoreProfileRequest, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(link, "ss://"))
	name := ""
	if idx := strings.Index(raw, "#"); idx >= 0 {
		name = decodeFragment(raw[idx+1:])
		raw = raw[:idx]
	}
	if idx := strings.Index(raw, "?"); idx >= 0 {
		raw = raw[:idx]
	}
	if strings.Contains(raw, "@") {
		u, err := url.Parse("ss://" + raw)
		if err == nil {
			port, convErr := strconv.Atoi(u.Port())
			if convErr != nil {
				return createCoreProfileRequest{}, errors.New("Shadowsocks 端口无效")
			}
			method, password, decErr := parseSSUserInfo(u.User.Username())
			if decErr != nil {
				return createCoreProfileRequest{}, decErr
			}
			return createCoreProfileRequest{
				Name:     name,
				Protocol: "shadowsocks",
				Status:   models.CoreProfileStatusEnabled,
				Server:   u.Hostname(),
				Port:     port,
				Method:   method,
				Password: password,
			}, nil
		}
	}
	decoded, err := decodeBase64Any(compactBase64(raw))
	if err != nil {
		return createCoreProfileRequest{}, fmt.Errorf("Shadowsocks 链接无效")
	}
	parts := strings.SplitN(string(decoded), "@", 2)
	if len(parts) != 2 {
		return createCoreProfileRequest{}, errors.New("Shadowsocks 链接格式错误")
	}
	method, password, err := splitMethodPassword(parts[0])
	if err != nil {
		return createCoreProfileRequest{}, err
	}
	host, portStr, err := netSplitHostPortLoose(parts[1])
	if err != nil {
		return createCoreProfileRequest{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return createCoreProfileRequest{}, errors.New("Shadowsocks 端口无效")
	}
	return createCoreProfileRequest{Name: name, Protocol: "shadowsocks", Status: models.CoreProfileStatusEnabled, Server: host, Port: port, Method: method, Password: password}, nil
}

func parseSimpleProxyShareLink(link string, protocol string) (createCoreProfileRequest, error) {
	u, err := url.Parse(link)
	if err != nil {
		return createCoreProfileRequest{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return createCoreProfileRequest{}, fmt.Errorf("%s 端口无效", strings.ToUpper(protocol))
	}
	password, _ := u.User.Password()
	return createCoreProfileRequest{
		Name:     decodeFragment(u.Fragment),
		Protocol: protocol,
		Status:   models.CoreProfileStatusEnabled,
		Server:   u.Hostname(),
		Port:     port,
		Username: u.User.Username(),
		Password: password,
	}, nil
}

func parseVMessShareLink(link string) (createCoreProfileRequest, error) {
	payload := strings.TrimSpace(strings.TrimPrefix(link, "vmess://"))
	// 提取节点名称（# 后面的 fragment），base64 payload 不包含 #
	name := ""
	if idx := strings.Index(payload, "#"); idx >= 0 {
		name = decodeFragment(strings.TrimSpace(payload[idx+1:]))
		payload = strings.TrimSpace(payload[:idx])
	}
	// 去掉 payload 末尾可能残留的 ? 参数（部分非标准链接）
	if idx := strings.Index(payload, "?"); idx >= 0 {
		payload = strings.TrimSpace(payload[:idx])
	}
	decoded, err := decodeBase64Any(compactBase64(payload))
	if err != nil {
		return createCoreProfileRequest{}, fmt.Errorf("VMess 链接解码失败: %s", err.Error())
	}
	// 清理 JSON 中可能存在的 BOM、前导垃圾字节
	jsonBytes := cleanJSONBytes(decoded)
	var item map[string]any
	if err := json.Unmarshal(jsonBytes, &item); err != nil {
		// 输出前64字节帮助调试
		preview := jsonBytes
		if len(preview) > 64 {
			preview = preview[:64]
		}
		return createCoreProfileRequest{}, fmt.Errorf("VMess JSON 无效 (前64字节: %q): %s", preview, err.Error())
	}
	port, _ := strconv.Atoi(coreImportStringValue(item["port"]))
	transport := firstNonEmpty(coreImportStringValue(item["net"]), coreImportStringValue(item["type"]), "tcp")
	// "type" 在 VMess 里是 header type（如 none/http），不是 transport，
	// transport 应优先取 "net" 字段
	headerType := coreImportStringValue(item["type"])
	if strings.EqualFold(headerType, "none") || strings.EqualFold(headerType, "auto") || headerType == "" {
		transport = firstNonEmpty(coreImportStringValue(item["net"]), "tcp")
	} else {
		transport = firstNonEmpty(coreImportStringValue(item["net"]), "tcp")
	}
	security := coreImportStringValue(item["tls"])
	if strings.EqualFold(security, "tls") {
		security = "tls"
	} else if strings.EqualFold(security, "reality") {
		security = "reality"
	} else {
		security = "none"
	}
	// 节点名优先用 # 后面解析到的，其次用 JSON 里的 ps/remark
	if name == "" {
		name = firstNonEmpty(coreImportStringValue(item["ps"]), coreImportStringValue(item["remark"]))
	}
	return createCoreProfileRequest{
		Name:             name,
		Protocol:         "vmess",
		Status:           models.CoreProfileStatusEnabled,
		Server:           firstNonEmpty(coreImportStringValue(item["add"]), coreImportStringValue(item["host"])),
		Port:             port,
		Transport:        transport,
		TLSMode:          security,
		SNI:              firstNonEmpty(coreImportStringValue(item["sni"]), coreImportStringValue(item["host"])),
		Host:             coreImportStringValue(item["host"]),
		Path:             coreImportStringValue(item["path"]),
		ServiceName:      coreImportStringValue(item["serviceName"]),
		AuthID:           coreImportStringValue(item["id"]),
		Fingerprint:      coreImportStringValue(item["fp"]),
		RealityPublicKey: coreImportStringValue(item["pbk"]),
		RealityShortID:   coreImportStringValue(item["sid"]),
		RealitySpiderX:   coreImportStringValue(item["spx"]),
	}, nil
}

func parseSSUserInfo(raw string) (string, string, error) {
	decoded, err := decodeBase64Any(compactBase64(raw))
	if err == nil {
		return splitMethodPassword(string(decoded))
	}
	return splitMethodPassword(raw)
}

func splitMethodPassword(raw string) (string, string, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", errors.New("Shadowsocks method/password 无效")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func decodeBase64Any(raw string) ([]byte, error) {
	candidates := []string{raw, compactBase64(raw)}
	for _, item := range candidates {
		for _, fn := range []func(string) ([]byte, error){
			base64.StdEncoding.DecodeString,
			base64.RawStdEncoding.DecodeString,
			base64.URLEncoding.DecodeString,
			base64.RawURLEncoding.DecodeString,
		} {
			if decoded, err := fn(item); err == nil {
				return decoded, nil
			}
		}
		if mod := len(item) % 4; mod != 0 {
			padded := item + strings.Repeat("=", 4-mod)
			for _, fn := range []func(string) ([]byte, error){
				base64.StdEncoding.DecodeString,
				base64.URLEncoding.DecodeString,
			} {
				if decoded, err := fn(padded); err == nil {
					return decoded, nil
				}
			}
		}
	}
	return nil, errors.New("base64 decode failed")
}

func compactBase64(raw string) string {
	// 只去掉换行、回车、制表符和空格，不处理 + 号
	// + 是合法的 base64 字符（标准编码），不能被去掉
	replacer := strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "")
	return replacer.Replace(strings.TrimSpace(raw))
}

func decodeFragment(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func decodePercent(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func decodePercentLoose(value string) string {
	if !strings.Contains(value, "%") {
		// 不含 % 就不做 URL 解码，避免把 + 误转成空格
		return value
	}
	// 用 PathUnescape 而不是 QueryUnescape，PathUnescape 不会把 + 转成空格
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func normalizeShareSecurityMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tls":
		return "tls"
	case "reality":
		return "reality"
	default:
		return "none"
	}
}

func parseBoolLoose(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func coreImportStringValue(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func netSplitHostPortLoose(value string) (string, string, error) {
	last := strings.LastIndex(value, ":")
	if last <= 0 || last >= len(value)-1 {
		return "", "", errors.New("地址格式无效")
	}
	return strings.TrimSpace(value[:last]), strings.TrimSpace(value[last+1:]), nil
}

func coreProfileRequestFingerprint(req createCoreProfileRequest) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Protocol)),
		strings.ToLower(strings.TrimSpace(req.Server)),
		strconv.Itoa(req.Port),
		strings.TrimSpace(req.AuthID),
		strings.TrimSpace(req.Password),
		strings.TrimSpace(req.Method),
		strings.TrimSpace(req.Username),
		strings.TrimSpace(req.Transport),
		strings.TrimSpace(req.Path),
		strings.TrimSpace(req.ServiceName),
	}, "|")
}

func coreProfileFingerprint(profile models.CoreProfile, plainAuthID, plainPassword string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(profile.Protocol)),
		strings.ToLower(strings.TrimSpace(profile.Server)),
		strconv.Itoa(profile.Port),
		strings.TrimSpace(plainAuthID),
		strings.TrimSpace(plainPassword),
		strings.TrimSpace(profile.Method),
		strings.TrimSpace(profile.Username),
		strings.TrimSpace(profile.Transport),
		strings.TrimSpace(profile.Path),
		strings.TrimSpace(profile.ServiceName),
	}, "|")
}

func tailFileLines(path string, maxLines int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

// cleanJSONBytes 清理 JSON 字节中可能存在的 BOM 和前导垃圾字节，
// 找到第一个 { 或 [ 作为 JSON 起始位置。
func cleanJSONBytes(data []byte) []byte {
	// 去掉 UTF-8 BOM (0xEF 0xBB 0xBF)
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	// 跳过前导非 JSON 字节，找到第一个 { 或 [
	for i, b := range data {
		if b == '{' || b == '[' {
			return data[i:]
		}
	}
	return data
}
