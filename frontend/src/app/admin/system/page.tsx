"use client";

import { useEffect, useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

interface SystemConfig {
  upstreamBaseURL: string;
  schedulerStrategy: string;
  maxRetries: number;
  maxConcurrency: number;
  requestTimeoutSecond: number;
  upstreamProxyURL: string;
  upstreamProxyId?: number;
  gatewayBaseURL: string;
  firstByteTimeoutMs: number;
  healthProbeTimeoutSecond: number;
  streamIdleTimeoutSecond: number;
  streamKeepAliveSecond: number;
  transportRetryCount: number;
  transportRetryBackoffMs: number;
  enableOpenAI: boolean;
  enableClaude: boolean;
  enableGemini: boolean;
  anonymousAccess: boolean;
}

interface ProxyTestRecord {
  success: boolean;
  statusCode?: number;
  responseTime?: number;
  message?: string;
  target?: string;
  testedAt: string;
  summary: string;
}

interface ProxyOption {
  id: number;
  name: string;
  group?: string;
  source?: string;
  managedBy?: string;
  status: string;
  type: string;
  proxyURL: string;
  urlPreview: string;
  lastTest?: ProxyTestRecord | null;
}

const defaultConfig: SystemConfig = {
  upstreamBaseURL: "https://integrate.api.nvidia.com/v1",
  schedulerStrategy: "weighted_round_robin",
  maxRetries: 5,
  maxConcurrency: 3,
  requestTimeoutSecond: 600,
  upstreamProxyURL: "",
  upstreamProxyId: 0,
  gatewayBaseURL: "http://127.0.0.1:18080",
  firstByteTimeoutMs: 90000,
  healthProbeTimeoutSecond: 45,
  streamIdleTimeoutSecond: 600,
  streamKeepAliveSecond: 15,
  transportRetryCount: 2,
  transportRetryBackoffMs: 300,
  enableOpenAI: true,
  enableClaude: true,
  enableGemini: true,
  anonymousAccess: false,
};

export default function SystemConfigPage() {
  const [config, setConfig] = useState<SystemConfig>(defaultConfig);
  const [proxyOptions, setProxyOptions] = useState<ProxyOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [testingProxyId, setTestingProxyId] = useState<number | null>(null);

  const loadProxyOptions = async () => {
    const proxyRes = await fetch("/api/system/proxy-options", { cache: "no-store" });
    const proxyData = await proxyRes.json().catch(() => null);
    if (proxyRes.ok) {
      setProxyOptions(Array.isArray(proxyData?.options) ? proxyData.options : []);
    }
  };

  useEffect(() => {
    const load = async () => {
      try {
        const configRes = await fetch("/api/system/config", { cache: "no-store" });
        const configData = await configRes.json().catch(() => null);
        if (!configRes.ok) {
          setError(configData?.error || "读取系统设置失败。");
          return;
        }
        await loadProxyOptions();
        setConfig({ ...defaultConfig, ...configData });
      } catch {
        setError("读取系统设置失败。");
      } finally {
        setLoading(false);
      }
    };
    void load();
  }, []);

  const updateField = <K extends keyof SystemConfig>(key: K, value: SystemConfig[K]) => {
    setConfig((current) => ({ ...current, [key]: value }));
  };

  const proxySelectValue = useMemo(() => {
    const currentId = Number(config.upstreamProxyId || 0);
    if (currentId > 0 && proxyOptions.some((item) => item.id === currentId)) return `proxy:${currentId}`;
    const current = config.upstreamProxyURL.trim();
    if (!current) return "__inherit__";
    if (current === "direct" || current === "none") return "__direct__";
    const matched = proxyOptions.find((item) => item.proxyURL === current);
    if (matched) return `proxy:${matched.id}`;
    return current ? "__custom__" : "__inherit__";
  }, [config.upstreamProxyId, config.upstreamProxyURL, proxyOptions]);

  const activeProxyOption = useMemo(() => {
    if (config.upstreamProxyId && config.upstreamProxyId > 0) {
      return proxyOptions.find((item) => item.id === config.upstreamProxyId) ?? null;
    }
    return proxyOptions.find((item) => item.proxyURL === config.upstreamProxyURL.trim()) ?? null;
  }, [config.upstreamProxyId, config.upstreamProxyURL, proxyOptions]);

  const handleProxySelect = (value: string) => {
    if (value === "__inherit__") {
      setConfig((current) => ({ ...current, upstreamProxyURL: "", upstreamProxyId: 0 }));
      return;
    }
    if (value === "__direct__") {
      setConfig((current) => ({ ...current, upstreamProxyURL: "direct", upstreamProxyId: 0 }));
      return;
    }
    if (value === "__custom__") {
      setConfig((current) => ({ ...current, upstreamProxyId: 0, upstreamProxyURL: proxySelectValue === "__custom__" ? current.upstreamProxyURL : "" }));
      return;
    }
    if (value.startsWith("proxy:")) {
      const nextProxyId = Number(value.slice(6));
      setConfig((current) => ({ ...current, upstreamProxyId: Number.isFinite(nextProxyId) ? nextProxyId : 0, upstreamProxyURL: "" }));
      return;
    }
    updateField("upstreamProxyURL", value);
  };

  const buildSavePayload = (nextConfig: SystemConfig) => {
    const payload: Record<string, unknown> = { ...nextConfig };
    if (Number(nextConfig.upstreamProxyId || 0) > 0) {
      payload.upstreamProxyId = Number(nextConfig.upstreamProxyId || 0);
      delete payload.upstreamProxyURL;
    } else {
      payload.upstreamProxyId = 0;
      payload.upstreamProxyURL = nextConfig.upstreamProxyURL;
    }
    return payload;
  };

  const saveConfig = async (nextConfig: SystemConfig, successMessage?: string) => {
    const res = await fetch("/api/system/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(buildSavePayload(nextConfig)),
    });
    const data = await res.json().catch(() => null);
    if (!res.ok) {
      throw new Error(data?.error || "保存失败。");
    }
    const normalized = { ...defaultConfig, ...(data?.config || nextConfig) };
    setConfig(normalized);
    setMessage(successMessage || data?.message || "系统设置已保存。");
  };

  const save = async () => {
    setSaving(true);
    setMessage(null);
    setError(null);
    try {
      await saveConfig(config);
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败。");
    } finally {
      setSaving(false);
    }
  };

  const applyProxyOption = async (option: ProxyOption) => {
    setSaving(true);
    setMessage(null);
    setError(null);
    const nextConfig = { ...config, upstreamProxyId: option.id, upstreamProxyURL: "" };
    try {
      await saveConfig(nextConfig, `已切换上游代理到：${option.name}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "切换上游代理失败。");
    } finally {
      setSaving(false);
    }
  };

  const testProxyOption = async (option: ProxyOption) => {
    setTestingProxyId(option.id);
    setMessage(null);
    setError(null);
    try {
      const res = await fetch("/api/proxies/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ proxyId: option.id }),
      });
      const data = await res.json().catch(() => null);
      if (!res.ok) {
        setError(data?.error || `测试代理 ${option.name} 失败。`);
        return;
      }
      const statusLabel = data?.success ? "成功" : "失败";
      const responseTime = typeof data?.response_time === "number" ? `，${data.response_time} ms` : "";
      setMessage(`代理 ${option.name} 测试${statusLabel}${responseTime}`);
      await loadProxyOptions();
    } catch {
      setError(`测试代理 ${option.name} 失败。`);
    } finally {
      setTestingProxyId(null);
    }
  };

  return (
    <div className="space-y-6">
      <section className="rounded-[30px] border border-slate-200/70 bg-white/90 p-8 shadow-sm">
        <div className="max-w-3xl">
          <div className="text-xs uppercase tracking-[0.24em] text-slate-400">系统设置</div>
          <h1 className="mt-3 text-3xl font-semibold tracking-tight text-slate-900">调整网关运行参数</h1>
          <p className="mt-3 text-sm leading-7 text-slate-500">
            {"这里可以设置上游地址、代理、重试次数、并发数、首包切换超时、健康探测超时和协议开关。保存后会立即生效；业务请求和健康检查都会共用这套上游代理。"}
          </p>
        </div>
      </section>

      {(message || error) && (
        <div className={`rounded-2xl border px-5 py-4 text-sm ${error ? "border-rose-200 bg-rose-50 text-rose-700" : "border-emerald-200 bg-emerald-50 text-emerald-700"}`}>
          {error || message}
        </div>
      )}

      {config.anonymousAccess ? (
        <div className="rounded-2xl border border-amber-200 bg-amber-50 px-5 py-4 text-sm leading-7 text-amber-800">
          当前实例仍允许匿名访问（旧配置保留）。建议创建自定义 API Key 后关闭匿名访问。
        </div>
      ) : null}

      <section className="grid gap-6 xl:grid-cols-2">
        <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
          <CardHeader>
            <CardTitle>基础参数</CardTitle>
            <CardDescription>这些设置决定网关如何访问上游和调度请求。</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <label className="space-y-2 block">
              <span className="text-sm text-slate-500">真实网关出口地址</span>
              <Input value={config.gatewayBaseURL} disabled />
            </label>
            <label className="space-y-2 block">
              <span className="text-sm text-slate-500">上游地址</span>
              <Input value={config.upstreamBaseURL} onChange={(e) => updateField("upstreamBaseURL", e.target.value)} placeholder="https://integrate.api.nvidia.com/v1" disabled={loading} />
            </label>
            <div className="space-y-3">
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">上游代理</span>
                <Select value={proxySelectValue} onChange={(e) => handleProxySelect(e.target.value)} disabled={loading || saving}>
                  <option value="__inherit__">继承系统环境变量（默认）</option>
                  <option value="__direct__">强制直连（direct）</option>
                  <option value="__custom__">自定义代理 URL</option>
                  {proxyOptions.map((item) => (
                    <option key={`${item.id}-${item.proxyURL}`} value={`proxy:${item.id}`}>
                      {`${item.status === "Enabled" ? "[启用]" : "[禁用]"} ${item.managedBy === "xray" ? "[XRAY] " : ""}${item.name} · ${item.urlPreview}`}
                    </option>
                  ))}
                </Select>
              </label>
              {proxySelectValue === "__custom__" ? (
                <label className="space-y-2 block">
                  <span className="text-sm text-slate-500">自定义代理 URL</span>
                  <Input
                    value={config.upstreamProxyURL}
                    onChange={(e) => updateField("upstreamProxyURL", e.target.value)}
                    placeholder="留空=继承环境变量；direct=直连；http://127.0.0.1:7890；socks5h://user:pass@127.0.0.1:1080"
                    disabled={loading || saving}
                  />
                </label>
              ) : null}
              {activeProxyOption ? (
                <div className="rounded-2xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800">
                  当前已选项目代理：{activeProxyOption.name} · {activeProxyOption.urlPreview}
                </div>
              ) : null}
              <p className="text-xs leading-6 text-slate-500">
                保存后，业务请求和健康检查都会统一走这里选中的代理。支持 <code>http://</code>、<code>https://</code>、<code>socks5://</code>、<code>socks5h://</code>；选择 XRAY 托管代理或代理池代理都可以。
              </p>
            </div>
            <div className="grid gap-4 md:grid-cols-2">
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">调度策略</span>
                <Input value={config.schedulerStrategy} onChange={(e) => updateField("schedulerStrategy", e.target.value)} disabled={loading} />
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">首包切换超时（毫秒）</span>
                <Input type="number" min="500" value={String(config.firstByteTimeoutMs)} onChange={(e) => updateField("firstByteTimeoutMs", Number(e.target.value))} disabled={loading} />
              </label>
            </div>
            <div className="grid gap-4 md:grid-cols-3 xl:grid-cols-6">
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">最大重试</span>
                <Input type="number" min="0" value={String(config.maxRetries)} onChange={(e) => updateField("maxRetries", Number(e.target.value))} disabled={loading} />
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">单 key 并发</span>
                <Input type="number" min="1" value={String(config.maxConcurrency)} onChange={(e) => updateField("maxConcurrency", Number(e.target.value))} disabled={loading} />
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">请求超时（秒）</span>
                <Input type="number" min="30" value={String(config.requestTimeoutSecond)} onChange={(e) => updateField("requestTimeoutSecond", Number(e.target.value))} disabled={loading} />
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">健康探测超时（秒）</span>
                <Input type="number" min="5" value={String(config.healthProbeTimeoutSecond)} onChange={(e) => updateField("healthProbeTimeoutSecond", Number(e.target.value))} disabled={loading} />
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">流式空闲超时（秒）</span>
                <Input type="number" min="30" value={String(config.streamIdleTimeoutSecond)} onChange={(e) => updateField("streamIdleTimeoutSecond", Number(e.target.value))} disabled={loading} />
                <span className="block text-xs text-slate-400">上游两个 chunk 之间最大允许的静默时间；Claude Code 长任务建议 ≥600。过短会被网关当成"僵尸流"提前发 message_stop，导致客户端误判已完成而中断后续对话。</span>
              </label>
              <label className="space-y-2 block">
                <span className="text-sm text-slate-500">流式心跳间隔（秒）</span>
                <Input type="number" min="0" value={String(config.streamKeepAliveSecond)} onChange={(e) => updateField("streamKeepAliveSecond", Number(e.target.value))} disabled={loading} />
                <span className="block text-xs text-slate-400">空闲时向客户端发送 SSE 注释帧防止 CDN/代理 RST；0 表示禁用，推荐 10~30。</span>
              </label>
            </div>
          </CardContent>
        </Card>

        <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
          <CardHeader>
            <CardTitle>协议与访问控制</CardTitle>
            <CardDescription>决定哪些协议开放，以及是否允许匿名访问。</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Toggle label="启用 OpenAI 出口" checked={config.enableOpenAI} onChange={(value) => updateField("enableOpenAI", value)} />
            <Toggle label="启用 Claude 出口" checked={config.enableClaude} onChange={(value) => updateField("enableClaude", value)} />
            <Toggle label="启用 Gemini 出口" checked={config.enableGemini} onChange={(value) => updateField("enableGemini", value)} />
            <Toggle label="允许匿名访问（没有自定义 API Key 时也能调用）" checked={config.anonymousAccess} onChange={(value) => updateField("anonymousAccess", value)} />
            <div className="rounded-2xl border border-dashed border-slate-200 bg-slate-50 px-4 py-4 text-sm text-slate-500">
              <div>录入上游 key 时，直接粘贴你拿到的完整值即可，例如：</div>
              <div className="mt-2 break-all font-mono text-slate-700">nvapi-fwRasMdcYM2U*******************************yfebnDf</div>
            </div>
          </CardContent>
        </Card>
      </section>

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>项目代理快速测试 / 一键设为上游</CardTitle>
          <CardDescription>这里列出项目内所有可直接选用的代理，包括代理池和 XRAY 托管代理。测试成功后可一键设为系统上游代理。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {proxyOptions.length === 0 ? (
            <div className="rounded-2xl border border-dashed border-slate-200 px-4 py-10 text-center text-sm text-slate-500">当前还没有可用代理，请先去代理管理或 Xray 节点页准备代理。</div>
          ) : (
            <div className="grid gap-3">
              {proxyOptions.map((item) => {
                const selected = Number(config.upstreamProxyId || 0) === item.id || (Number(config.upstreamProxyId || 0) === 0 && config.upstreamProxyURL.trim() === item.proxyURL);
                return (
                  <div key={`${item.id}-${item.proxyURL}`} className={`rounded-2xl border px-4 py-4 ${selected ? "border-emerald-200 bg-emerald-50/70" : "border-slate-200 bg-slate-50/70"}`}>
                    <div className="flex flex-col gap-3 xl:flex-row xl:items-start xl:justify-between">
                      <div className="space-y-2">
                        <div className="flex flex-wrap items-center gap-2">
                          <div className="text-sm font-semibold text-slate-900">{item.name}</div>
                          <span className={`rounded-full px-2 py-0.5 text-[11px] ${item.status === "Enabled" ? "bg-emerald-100 text-emerald-700" : "bg-slate-200 text-slate-600"}`}>{item.status === "Enabled" ? "启用" : "禁用"}</span>
                          {item.managedBy === "xray" ? <span className="rounded-full bg-sky-100 px-2 py-0.5 text-[11px] text-sky-700">XRAY 托管</span> : null}
                          {selected ? <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-[11px] text-emerald-700">当前上游</span> : null}
                        </div>
                        <div className="text-xs text-slate-500">{displayProxyPreview(item)}</div>
                        <div className="text-xs text-slate-500">分组：{item.group || "-"} · 来源：{item.source || "-"} · 类型：{item.type}</div>
                        {item.lastTest ? (
                          <div className={`rounded-xl border px-3 py-2 text-xs ${item.lastTest.success ? "border-emerald-200 bg-emerald-50 text-emerald-700" : "border-amber-200 bg-amber-50 text-amber-700"}`}>
                            最近测试：{item.lastTest.summary} · {new Date(item.lastTest.testedAt).toLocaleString()}
                            {typeof item.lastTest.responseTime === "number" ? ` · ${item.lastTest.responseTime} ms` : ""}
                            {item.lastTest.message ? ` · ${item.lastTest.message}` : ""}
                          </div>
                        ) : (
                          <div className="text-xs text-slate-400">暂无测试记录</div>
                        )}
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button type="button" variant="outline" size="sm" onClick={() => testProxyOption(item)} disabled={loading || saving || testingProxyId === item.id}>{testingProxyId === item.id ? "测试中..." : "测试此代理"}</Button>
                        <Button type="button" size="sm" onClick={() => applyProxyOption(item)} disabled={loading || saving}>{saving && selected ? "保存中..." : "设为上游并保存"}</Button>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>常用接口路径</CardTitle>
          <CardDescription>便于核对不同客户端应该走哪条路由。</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 text-sm text-slate-600 md:grid-cols-2 xl:grid-cols-3">
          <PathItem label="OpenAI 模型列表" value="/v1/models" />
          <PathItem label="OpenAI 对话" value="/v1/chat/completions" />
          <PathItem label="OpenAI Responses" value="/v1/responses" />
          <PathItem label="OpenAI Embeddings" value="/v1/embeddings" />
          <PathItem label="Claude Messages" value="/anthropic/v1/messages" />
          <PathItem label="Gemini Stream" value="/v1beta/models/{model}:streamGenerateContent" />
        </CardContent>
      </Card>

      <div className="flex gap-3">
        <Button onClick={save} disabled={loading || saving}>{saving ? "保存中..." : "保存设置"}</Button>
      </div>
    </div>
  );
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (value: boolean) => void;
}) {
  return (
    <label className="flex items-center justify-between rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-700">
      <span>{label}</span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  );
}

function PathItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3">
      <div className="text-xs uppercase tracking-[0.2em] text-slate-400">{label}</div>
      <div className="mt-2 break-all font-mono text-slate-800">{value}</div>
    </div>
  );
}


function displayProxyPreview(item: ProxyOption) {
  if (item.managedBy === "xray") return "内部 Xray 出口（仅本机回环，不对外开放）";
  return item.urlPreview;
}
