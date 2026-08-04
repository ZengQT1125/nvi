"use client";

import { useEffect, useMemo, useState } from "react";
import useSWR from "swr";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

const fetcher = async (url: string) => {
  const res = await fetch(url, { cache: "no-store" });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error || "request_failed");
  return data;
};

interface ProxyTestRecord {
  success: boolean;
  statusCode?: number;
  responseTime?: number;
  message?: string;
  target?: string;
  testedAt: string;
  summary: string;
}

interface CoreProfile {
  id: number;
  name: string;
  protocol: string;
  status: string;
  server: string;
  port: number;
  localPort: number;
  localProxyURL: string;
  managedProxyId?: number;
  transport?: string;
  tlsMode?: string;
  sni?: string;
  allowInsecure?: boolean;
  host?: string;
  path?: string;
  serviceName?: string;
  flow?: string;
  method?: string;
  username?: string;
  hasPassword: boolean;
  hasAuthId: boolean;
  fingerprint?: string;
  realityPublicKey?: string;
  realityShortId?: string;
  realitySpiderX?: string;
  remarks?: string;
  lastTest?: ProxyTestRecord | null;
  createdAt: string;
  updatedAt: string;
}

interface XrayRuntime {
  running: boolean;
  platform: string;
  binaryPath?: string;
  configPath?: string;
  logPath?: string;
  version?: string;
  enabledProfiles: number;
  managedProxies: number;
  lastAppliedAt?: string;
  startedAt?: string;
  lastError?: string;
  activePorts?: number[];
}

interface CoreProfilesResponse {
  profiles: CoreProfile[];
  runtime: XrayRuntime;
}

interface ImportCoreProfilesResponse {
  message: string;
  imported: number;
  skipped: number;
  warnings?: string[];
}

interface XrayLogsResponse {
  path?: string;
  lastError?: string;
  content: string;
}

interface BatchTestCoreProfilesResponse {
  message: string;
  total: number;
  successCount: number;
  failedCount: number;
  target?: string;
}

interface CoreFormState {
  name: string;
  protocol: string;
  status: string;
  server: string;
  port: string;
  localPort: string;
  transport: string;
  tlsMode: string;
  sni: string;
  allowInsecure: boolean;
  host: string;
  path: string;
  serviceName: string;
  flow: string;
  method: string;
  username: string;
  password: string;
  authId: string;
  fingerprint: string;
  realityPublicKey: string;
  realityShortId: string;
  realitySpiderX: string;
  remarks: string;
}

const emptyForm: CoreFormState = {
  name: "",
  protocol: "vless",
  status: "Enabled",
  server: "",
  port: "443",
  localPort: "",
  transport: "tcp",
  tlsMode: "none",
  sni: "",
  allowInsecure: false,
  host: "",
  path: "",
  serviceName: "",
  flow: "",
  method: "aes-256-gcm",
  username: "",
  password: "",
  authId: "",
  fingerprint: "chrome",
  realityPublicKey: "",
  realityShortId: "",
  realitySpiderX: "",
  remarks: "",
};

export default function CorePage() {
  const { data, error, mutate } = useSWR<CoreProfilesResponse>("/api/core/profiles", fetcher, { refreshInterval: 4000 });
  const [logLines, setLogLines] = useState("200");
  const { data: logData, mutate: mutateLogs } = useSWR<XrayLogsResponse>(`/api/core/runtime/logs?lines=${encodeURIComponent(logLines)}`, fetcher, { refreshInterval: 5000 });

  const [form, setForm] = useState<CoreFormState>(emptyForm);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [reloading, setReloading] = useState(false);
  const [importing, setImporting] = useState(false);
  const [batchTesting, setBatchTesting] = useState(false);
  const [batchDeleting, setBatchDeleting] = useState(false);
  const [clearingLogs, setClearingLogs] = useState(false);
  const [sortMode, setSortMode] = useState<"latency" | "name">("latency");
  const [importUrl, setImportUrl] = useState("");
  const [importText, setImportText] = useState("");
  const [selectedProfileIds, setSelectedProfileIds] = useState<number[]>([]);
  const [importWarnings, setImportWarnings] = useState<string[]>([]);
  const [message, setMessage] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  useEffect(() => {
    if (!message && !errorMessage) return;
    const timer = window.setTimeout(() => {
      setMessage(null);
      setErrorMessage(null);
    }, 4000);
    return () => window.clearTimeout(timer);
  }, [message, errorMessage]);

  const profiles = useMemo(() => {
    const items = [...(data?.profiles ?? [])];
    if (sortMode === "name") {
      return items.sort((a, b) => a.name.localeCompare(b.name));
    }
    const rank = (profile: CoreProfile) => {
      if (profile.status === "Disabled") return 3;
      if (!profile.lastTest) return 2;
      return profile.lastTest.success ? 0 : 1;
    };
    const latency = (profile: CoreProfile) => profile.lastTest?.responseTime ?? Number.MAX_SAFE_INTEGER;
    const testedAt = (profile: CoreProfile) => (profile.lastTest?.testedAt ? new Date(profile.lastTest.testedAt).getTime() : 0);
    return items.sort((a, b) => {
      if (rank(a) !== rank(b)) return rank(a) - rank(b);
      if (latency(a) !== latency(b)) return latency(a) - latency(b);
      if (testedAt(a) !== testedAt(b)) return testedAt(b) - testedAt(a);
      return a.name.localeCompare(b.name);
    });
  }, [data?.profiles, sortMode]);
  const runtime = data?.runtime;
  const currentEditingProfile = useMemo(() => profiles.find((item) => item.id === editingId) ?? null, [editingId, profiles]);
  const activePorts = runtime?.activePorts ?? [];
  const existingProfileIdSet = useMemo(() => new Set(profiles.map((item) => item.id)), [profiles]);
  const effectiveSelectedProfileIds = useMemo(() => selectedProfileIds.filter((id) => existingProfileIdSet.has(id)), [existingProfileIdSet, selectedProfileIds]);
  const selectedProfileIdSet = useMemo(() => new Set(effectiveSelectedProfileIds), [effectiveSelectedProfileIds]);
  const failedTestedProfileIds = useMemo(() => profiles.filter((profile) => profile.lastTest?.success === false).map((profile) => profile.id), [profiles]);
  const allVisibleSelected = profiles.length > 0 && effectiveSelectedProfileIds.length === profiles.length;

  const resetMessages = () => {
    setMessage(null);
    setErrorMessage(null);
  };

  const validateForm = () => {
    if (!form.name.trim()) return "\u8282\u70b9\u540d\u79f0\u4e0d\u80fd\u4e3a\u7a7a\u3002";
    if (!form.server.trim()) return "\u670d\u52a1\u5668\u5730\u5740\u4e0d\u80fd\u4e3a\u7a7a\u3002";
    const serverPort = Number(form.port);
    if (!Number.isFinite(serverPort) || serverPort <= 0 || serverPort > 65535) return "\u670d\u52a1\u5668\u7aef\u53e3\u5fc5\u987b\u5728 1-65535 \u4e4b\u95f4\u3002";
    if (form.localPort.trim()) {
      const localPort = Number(form.localPort);
      if (!Number.isFinite(localPort) || localPort <= 0 || localPort > 65535) return "\u672c\u5730\u7aef\u53e3\u5fc5\u987b\u5728 1-65535 \u4e4b\u95f4\u3002";
    }
    if ((form.protocol === "vless" || form.protocol === "vmess") && !form.authId.trim() && !(editingId && currentEditingProfile?.hasAuthId)) return "VLESS / VMess \u5fc5\u987b\u586b\u5199 UUID / Auth ID\u3002";
    if (form.protocol === "trojan" && !form.password.trim() && !(editingId && currentEditingProfile?.hasPassword)) return "Trojan \u5fc5\u987b\u586b\u5199\u5bc6\u7801\u3002";
    if (form.protocol === "shadowsocks") {
      if (!form.method.trim()) return "Shadowsocks \u5fc5\u987b\u586b\u5199\u52a0\u5bc6\u65b9\u6cd5\u3002";
      if (!form.password.trim() && !(editingId && currentEditingProfile?.hasPassword)) return "Shadowsocks \u5fc5\u987b\u586b\u5199\u5bc6\u7801\u3002";
    }
    if (form.tlsMode === "reality") {
      if (!form.sni.trim()) return "Reality \u6a21\u5f0f\u5fc5\u987b\u586b\u5199 SNI\u3002";
      if (!form.realityPublicKey.trim()) return "Reality \u6a21\u5f0f\u5fc5\u987b\u586b\u5199 Public Key\u3002";
    }
    return null;
  };
  const buildPayload = () => ({
    name: form.name.trim(),
    protocol: form.protocol,
    status: form.status,
    server: form.server.trim(),
    port: Number(form.port),
    localPort: form.localPort.trim() ? Number(form.localPort) : 0,
    transport: form.transport,
    tlsMode: form.tlsMode,
    sni: form.sni.trim(),
    allowInsecure: form.allowInsecure,
    host: form.host.trim(),
    path: form.path.trim(),
    serviceName: form.serviceName.trim(),
    flow: form.flow.trim(),
    method: form.method.trim(),
    username: form.username.trim(),
    password: form.password,
    authId: form.authId,
    fingerprint: form.fingerprint.trim(),
    realityPublicKey: form.realityPublicKey.trim(),
    realityShortId: form.realityShortId.trim(),
    realitySpiderX: form.realitySpiderX.trim(),
    remarks: form.remarks.trim(),
  });

  const submitProfile = async () => {
    resetMessages();
    const validationError = validateForm();
    if (validationError) {
      setErrorMessage(validationError);
      return;
    }
    setSubmitting(true);
    try {
      const url = editingId ? `/api/core/profiles/${editingId}` : "/api/core/profiles";
      const method = editingId ? "PUT" : "POST";
      const res = await fetch(url, {
        method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(buildPayload()),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || (editingId ? "\u66f4\u65b0\u6838\u5fc3\u8282\u70b9\u5931\u8d25\u3002" : "\u521b\u5efa\u6838\u5fc3\u8282\u70b9\u5931\u8d25\u3002"));
        return;
      }
      setMessage(payload?.message || (editingId ? "\u6838\u5fc3\u8282\u70b9\u66f4\u65b0\u6210\u529f\u3002" : "\u6838\u5fc3\u8282\u70b9\u521b\u5efa\u6210\u529f\u3002"));
      setForm(emptyForm);
      setEditingId(null);
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setSubmitting(false);
    }
  };

  const importProfiles = async () => {
    resetMessages();
    setImportWarnings([]);
    if (!importUrl.trim() && !importText.trim()) {
      setErrorMessage("\u8bf7\u586b\u5199\u8ba2\u9605 URL \u6216\u7c98\u8d34\u5206\u4eab\u94fe\u63a5\u3002\n\u652f\u6301 vmess:// vless:// trojan:// ss:// socks:// http://");
      return;
    }
    setImporting(true);
    try {
      const res = await fetch("/api/core/profiles/import", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ subscriptionUrl: importUrl.trim(), rawText: importText }),
      });
      const payload = await res.json().catch(() => null) as ImportCoreProfilesResponse | null;
      if (!res.ok) {
        setErrorMessage((payload as { error?: string } | null)?.error || "\u5bfc\u5165\u6838\u5fc3\u8282\u70b9\u5931\u8d25\u3002");
        return;
      }
      setMessage(payload?.message || "\u5bfc\u5165\u5b8c\u6210\u3002");
      setImportWarnings(payload?.warnings ?? []);
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setImporting(false);
    }
  };

  const startEdit = (profile: CoreProfile) => {
    setEditingId(profile.id);
    setForm({
      name: profile.name,
      protocol: profile.protocol,
      status: profile.status,
      server: profile.server,
      port: String(profile.port),
      localPort: profile.localPort ? String(profile.localPort) : "",
      transport: profile.transport || "tcp",
      tlsMode: profile.tlsMode || "none",
      sni: profile.sni || "",
      allowInsecure: profile.allowInsecure ?? false,
      host: profile.host || "",
      path: profile.path || "",
      serviceName: profile.serviceName || "",
      flow: profile.flow || "",
      method: profile.method || "aes-256-gcm",
      username: profile.username || "",
      password: "",
      authId: "",
      fingerprint: profile.fingerprint || "chrome",
      realityPublicKey: profile.realityPublicKey || "",
      realityShortId: profile.realityShortId || "",
      realitySpiderX: profile.realitySpiderX || "",
      remarks: profile.remarks || "",
    });
  };

  const cancelEdit = () => {
    setEditingId(null);
    setForm(emptyForm);
  };

  const deleteProfile = async (profile: CoreProfile) => {
    resetMessages();
    if (!window.confirm(`\u786e\u5b9a\u8981\u5220\u9664\u6838\u5fc3\u8282\u70b9\u300c${profile.name}\u300d\u5417\uff1f\u7ed1\u5b9a\u5230\u5b83\u7684\u672c\u5730\u4ee3\u7406\u4f1a\u4e00\u8d77\u79fb\u9664\u3002`)) return;
    setBusyId(profile.id);
    try {
      const res = await fetch(`/api/core/profiles/${profile.id}`, { method: "DELETE" });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "\u5220\u9664\u6838\u5fc3\u8282\u70b9\u5931\u8d25\u3002");
        return;
      }
      setMessage(payload?.message || "\u6838\u5fc3\u8282\u70b9\u5df2\u5220\u9664\u3002");
      if (editingId === profile.id) cancelEdit();
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBusyId(null);
    }
  };

  const toggleStatus = async (profile: CoreProfile) => {
    resetMessages();
    setBusyId(profile.id);
    try {
      const nextStatus = profile.status === "Disabled" ? "Enabled" : "Disabled";
      const res = await fetch(`/api/core/profiles/${profile.id}/status`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: nextStatus }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "\u66f4\u65b0\u6838\u5fc3\u8282\u70b9\u72b6\u6001\u5931\u8d25\u3002");
        return;
      }
      setMessage(payload?.message || "\u6838\u5fc3\u8282\u70b9\u72b6\u6001\u5df2\u66f4\u65b0\u3002");
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBusyId(null);
    }
  };

  const testProfile = async (profile: CoreProfile) => {
    resetMessages();
    setBusyId(profile.id);
    try {
      const res = await fetch(`/api/core/profiles/${profile.id}/test`, { method: "POST" });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "测试核心节点失败。");
        return;
      }
      setMessage(payload?.message || "核心节点测试已完成。");
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBusyId(null);
    }
  };

  const runBatchTest = async () => {
    resetMessages();
    if (effectiveSelectedProfileIds.length === 0) {
      setErrorMessage("请先在核心节点列表中至少选择一个节点。\n批量测试目标为 NVIDIA 官方的 /v1/models 接口。");
      return;
    }
    setBatchTesting(true);
    try {
      const res = await fetch("/api/core/profiles/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: effectiveSelectedProfileIds }),
      });
      const payload = await res.json().catch(() => null) as BatchTestCoreProfilesResponse | { error?: string } | null;
      if (!res.ok) {
        const errorPayload = payload as { error?: string } | null;
        setErrorMessage(errorPayload?.error || "批量测试核心节点失败。");
        return;
      }
      const successPayload = payload as BatchTestCoreProfilesResponse | null;
      setMessage(successPayload?.message || "批量测试完成。");
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBatchTesting(false);
    }
  };

  const deleteBatchProfiles = async () => {
    resetMessages();
    if (effectiveSelectedProfileIds.length === 0) {
      setErrorMessage("请先在核心节点列表中至少选择一个节点。");
      return;
    }
    if (!window.confirm(`确定要批量删除 ${effectiveSelectedProfileIds.length} 个核心节点吗？对应的托管代理也会一并删除，并自动解绑相关 key。`)) return;
    setBatchDeleting(true);
    try {
      const res = await fetch("/api/core/profiles/batch", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: effectiveSelectedProfileIds }),
      });
      const payload = await res.json().catch(() => null) as { error?: string; message?: string } | null;
      if (!res.ok) {
        setErrorMessage(payload?.error || "批量删除核心节点失败。");
        return;
      }
      setMessage(payload?.message || "批量删除完成。");
      setSelectedProfileIds([]);
      if (editingId !== null && effectiveSelectedProfileIds.includes(editingId)) {
        cancelEdit();
      }
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBatchDeleting(false);
    }
  };

  const deleteFailedProfiles = async () => {
    resetMessages();
    if (failedTestedProfileIds.length === 0) {
      setErrorMessage("当前没有可删除的测速失败节点。");
      return;
    }
    if (!window.confirm(`确定要删除全部 ${failedTestedProfileIds.length} 个测速失败节点吗？对应的托管代理也会一并删除，并自动解绑相关 key。`)) return;
    setBatchDeleting(true);
    try {
      const res = await fetch("/api/core/profiles/batch", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: failedTestedProfileIds }),
      });
      const payload = await res.json().catch(() => null) as { error?: string; message?: string } | null;
      if (!res.ok) {
        setErrorMessage(payload?.error || "删除测速失败节点失败。");
        return;
      }
      setMessage(payload?.message || "测速失败节点已删除。");
      setSelectedProfileIds((current) => current.filter((id) => !failedTestedProfileIds.includes(id)));
      if (editingId !== null && failedTestedProfileIds.includes(editingId)) {
        cancelEdit();
      }
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setBatchDeleting(false);
    }
  };
  const reloadRuntime = async () => {
    resetMessages();
    setReloading(true);
    try {
      const res = await fetch("/api/core/runtime/reload", { method: "POST" });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "重载 Xray 失败。");
        return;
      }
      setMessage(payload?.message || "Xray 已重载。");
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setReloading(false);
    }
  };
  const clearLogs = async () => {
    resetMessages();
    if (!window.confirm("确定要清空当前 Xray 运行日志吗？")) return;
    setClearingLogs(true);
    try {
      const res = await fetch("/api/core/runtime/logs", { method: "DELETE" });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "清空 Xray 日志失败。");
        return;
      }
      setMessage(payload?.message || "Xray 日志已清空。");
      await Promise.all([mutate(), mutateLogs()]);
    } finally {
      setClearingLogs(false);
    }
  };

  const copyLocalProxy = async (profile: CoreProfile) => {
    try {
      await navigator.clipboard.writeText(profile.localProxyURL);
      setMessage(`已复制 ${profile.localProxyURL}`);
    } catch {
      setErrorMessage("复制本地代理地址失败，请手动复制。");
    }
  };

  const toggleProfileSelection = (id: number) => {
    setSelectedProfileIds((current) => (current.includes(id) ? current.filter((item) => item !== id) : [...current, id]));
  };

  const toggleSelectAllVisible = () => {
    setSelectedProfileIds(allVisibleSelected ? [] : profiles.map((profile) => profile.id));
  };

  const showAuthIdField = form.protocol === "vless" || form.protocol === "vmess";
  const showPasswordField = ["trojan", "shadowsocks", "socks", "http"].includes(form.protocol);
  const showMethodField = form.protocol === "shadowsocks";
  const showUserField = form.protocol === "socks" || form.protocol === "http";
  const showFlowField = form.protocol === "vless";
  const showTransportFields = ["vless", "vmess", "trojan", "shadowsocks"].includes(form.protocol);
  return (
    <div className="space-y-6">
      {message ? <div className="rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-700">{message}</div> : null}
      {errorMessage ? <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 whitespace-pre-line">{errorMessage}</div> : null}

      <Card className="border border-slate-200/70 bg-white/95 shadow-sm">
        <CardHeader>
          <CardTitle>{"Xray 核心运行时"}</CardTitle>
          <CardDescription>{"项目内置托管的多协议节点池，支持 Windows、Linux 与 Docker。运行时会自动下载官方核心并生成本地 SOCKS5 出口。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <RuntimeMetricCard label="运行状态" value={runtime?.running ? "运行中" : "未运行"} tone={runtime?.running ? "success" : "neutral"} />
            <RuntimeMetricCard label="平台" value={runtime?.platform || "-"} tone="neutral" />
            <RuntimeMetricCard label="启用节点" value={String(runtime?.enabledProfiles ?? 0)} tone="neutral" />
            <RuntimeMetricCard label="托管代理" value={String(runtime?.managedProxies ?? 0)} tone="neutral" />
          </div>

          <div className="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
            <div className="grid gap-4">
              <div className="rounded-3xl border border-slate-200 bg-slate-50/80 p-4">
                <div className="flex items-center justify-between gap-3">
                  <div className="text-sm font-medium text-slate-900">{"生效端口"}</div>
                  <Badge variant="secondary">{activePorts.length ? `${activePorts.length} 个` : "暂无"}</Badge>
                </div>
                <div className="mt-3 max-h-32 overflow-auto pr-1">
                  {activePorts.length > 0 ? (
                    <div className="flex flex-wrap gap-2">
                      {activePorts.map((port, index) => (
                        <span key={port} className="rounded-full border border-slate-200 bg-white px-3 py-1 text-xs font-medium text-slate-700">
                          {`内部出口 #${index + 1}`}
                        </span>
                      ))}
                    </div>
                  ) : (
                    <div className="rounded-2xl border border-dashed border-slate-200 bg-white px-4 py-5 text-sm text-slate-500">{"当前没有已生效的本地端口。"}</div>
                  )}
                </div>
              </div>

              <div className="rounded-3xl border border-slate-200 bg-slate-50/80 p-4">
                <div className="text-sm font-medium text-slate-900">{"运行文件"}</div>
                <div className="mt-3 grid gap-3 text-xs text-slate-500">
                  <PathRow label="核心版本" value={runtime?.version || "未下载 / 未解析"} />
                  <PathRow label="Binary" value={runtime?.binaryPath || "-"} />
                  <PathRow label="Config" value={runtime?.configPath || "-"} />
                  <PathRow label="Log" value={runtime?.logPath || "-"} />
                </div>
              </div>
            </div>

            <div className="grid gap-4">
              <div className="rounded-3xl border border-slate-200 bg-slate-50/80 p-4">
                <div className="text-sm font-medium text-slate-900">{"最近状态"}</div>
                <div className="mt-3 space-y-3 text-sm text-slate-600">
                  <InfoRow label="最近应用" value={runtime?.lastAppliedAt ? formatDate(runtime.lastAppliedAt) : "暂无"} />
                  <InfoRow label="最近启动" value={runtime?.startedAt ? formatDate(runtime.startedAt) : "暂无"} />
                  <InfoRow label="本地出口" value={activePorts.length > 0 ? `socks5://127.0.0.1:${activePorts[0]}` : "尚未生成"} />
                </div>
              </div>

              <div className="rounded-3xl border border-slate-200 bg-slate-50/80 p-4">
                <div className="text-sm font-medium text-slate-900">{"运行操作"}</div>
                <div className="mt-3 flex flex-wrap gap-3">
                  <Button type="button" onClick={reloadRuntime} disabled={reloading}>{reloading ? "重载中..." : "重载 Xray"}</Button>
                </div>
                <div className="mt-3 text-xs text-slate-500">{"说明：这里启动的是项目目录内的 Xray 运行时，不依赖你本机 v2rayN 的核心。"}</div>
              </div>
            </div>
          </div>

          {runtime?.lastError ? (
            <div className="rounded-3xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700">
              <div className="font-medium">{"最近错误"}</div>
              <div className="mt-1 break-all whitespace-pre-wrap">{runtime.lastError}</div>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/95 shadow-sm">
        <CardHeader>
          <CardTitle>{"订阅导入 / 分享链接解析"}</CardTitle>
          <CardDescription>{"支持 vmess://、vless://、trojan://、ss://、socks://、http://，并自动识别 Base64 / URL 编码 / HTML 转义后的订阅内容。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Input value={importUrl} onChange={(e) => setImportUrl(e.target.value)} placeholder="订阅 URL（可选）" />
          <textarea className="min-h-[180px] w-full rounded-3xl border border-slate-200 px-4 py-3 text-sm text-slate-900 outline-none transition focus:border-sky-300 focus:ring-4 focus:ring-sky-50 placeholder:text-slate-400" value={importText} onChange={(e) => setImportText(e.target.value)} placeholder={"粘贴分享链接或订阅内容，一行一个或整段均可。\n支持：vmess:// vless:// trojan:// ss:// socks:// http://\n也支持 Base64 / URL 编码 / HTML 转义后的订阅文本"} />
          <div className="flex flex-wrap gap-3">
            <Button type="button" onClick={importProfiles} disabled={importing}>{importing ? "导入中..." : "开始导入"}</Button>
          </div>
          {importWarnings.length > 0 ? (
            <div className="rounded-3xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700">
              <div className="font-medium">{"导入警告"}</div>
              <div className="mt-3 max-h-56 space-y-2 overflow-auto pr-1 leading-6">
                {importWarnings.map((item, index) => <div key={`${item}-${index}`}>{item}</div>)}
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>{editingId ? "编辑核心节点" : "新增核心节点"}</CardTitle>
          <CardDescription>保存后会自动同步出一个本地 SOCKS5 托管代理，供现有代理池和 Key 绑定复用。</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="节点名称" />
            <Select value={form.protocol} onChange={(e) => setForm({ ...form, protocol: e.target.value })}>
              <option value="vless">vless</option>
              <option value="vmess">vmess</option>
              <option value="shadowsocks">shadowsocks</option>
              <option value="trojan">trojan</option>
              <option value="socks">socks</option>
              <option value="http">http</option>
            </Select>
            <Input value={form.server} onChange={(e) => setForm({ ...form, server: e.target.value })} placeholder="服务器地址 / IP" />
            <Input value={form.port} onChange={(e) => setForm({ ...form, port: e.target.value })} placeholder="服务器端口" />
            <Input value={form.localPort} onChange={(e) => setForm({ ...form, localPort: e.target.value })} placeholder="本地端口（留空自动分配）" />
            <Select value={form.status} onChange={(e) => setForm({ ...form, status: e.target.value })}>
              <option value="Enabled">已启用</option>
              <option value="Disabled">已禁用</option>
            </Select>
            {showTransportFields ? (
              <Select value={form.transport} onChange={(e) => setForm({ ...form, transport: e.target.value })}>
                <option value="tcp">tcp</option>
                <option value="ws">ws</option>
                <option value="grpc">grpc</option>
              </Select>
            ) : <div />}
            {showTransportFields ? (
              <Select value={form.tlsMode} onChange={(e) => setForm({ ...form, tlsMode: e.target.value })}>
                <option value="none">none</option>
                <option value="tls">tls</option>
                <option value="reality">reality</option>
              </Select>
            ) : <div />}
          </div>
          <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            {showAuthIdField ? <Input value={form.authId} onChange={(e) => setForm({ ...form, authId: e.target.value })} placeholder={editingId ? "UUID / Auth ID（留空保留）" : "UUID / Auth ID"} /> : null}
            {showPasswordField ? <Input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} placeholder={editingId ? "密码（留空保留）" : "密码"} /> : null}
            {showMethodField ? <Input value={form.method} onChange={(e) => setForm({ ...form, method: e.target.value })} placeholder="加密方法，例如 aes-256-gcm" /> : null}
            {showUserField ? <Input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} placeholder="用户名（可选）" /> : null}
            {showFlowField ? <Input value={form.flow} onChange={(e) => setForm({ ...form, flow: e.target.value })} placeholder="Flow（可选，如 xtls-rprx-vision）" /> : null}
            {showTransportFields ? <Input value={form.sni} onChange={(e) => setForm({ ...form, sni: e.target.value })} placeholder="SNI / ServerName（可选）" /> : null}
            {showTransportFields && form.transport === "ws" ? <Input value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} placeholder="WS Host（可选）" /> : null}
            {showTransportFields && form.transport === "ws" ? <Input value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} placeholder="WS Path（可选）" /> : null}
            {showTransportFields && form.transport === "grpc" ? <Input value={form.serviceName} onChange={(e) => setForm({ ...form, serviceName: e.target.value })} placeholder="gRPC ServiceName（可选）" /> : null}
          </div>

          {form.tlsMode === "reality" ? (
            <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <Input value={form.realityPublicKey} onChange={(e) => setForm({ ...form, realityPublicKey: e.target.value })} placeholder="Reality 公钥" />
              <Input value={form.realityShortId} onChange={(e) => setForm({ ...form, realityShortId: e.target.value })} placeholder="Reality Short ID（可选）" />
              <Input value={form.fingerprint} onChange={(e) => setForm({ ...form, fingerprint: e.target.value })} placeholder="Fingerprint（默认 chrome）" />
              <Input value={form.realitySpiderX} onChange={(e) => setForm({ ...form, realitySpiderX: e.target.value })} placeholder="Reality SpiderX（可选）" />
            </div>
          ) : null}
          <div className="mt-4 grid gap-4 md:grid-cols-2">
            <label className="flex items-center gap-3 rounded-2xl border border-slate-200 px-4 py-3 text-sm text-slate-600">
              {"允许不安全证书"}
              <Switch checked={form.allowInsecure} onCheckedChange={(checked) => setForm({ ...form, allowInsecure: checked })} />
            </label>
            <Input value={form.remarks} onChange={(e) => setForm({ ...form, remarks: e.target.value })} placeholder="备注（可选）" />
          </div>
          <div className="mt-4 flex flex-wrap gap-3">
            <Button type="button" onClick={submitProfile} disabled={submitting}>{submitting ? "保存中..." : editingId ? "保存修改" : "新增节点"}</Button>
            {editingId ? <Button type="button" variant="outline" onClick={cancelEdit}>{"取消编辑"}</Button> : null}
          </div>
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/95 shadow-sm">
        <CardHeader>
          <CardTitle>{"Xray 运行日志"}</CardTitle>
          <CardDescription>{"这里显示项目内 Xray 的最近日志尾部，便于排查节点握手失败、配置错误、下载失败，以及到 NVIDIA 官方接口的连通性问题。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center gap-3">
            <Select value={logLines} onChange={(e) => setLogLines(e.target.value)}>
              <option value="100">最近 100 行</option>
              <option value="200">最近 200 行</option>
              <option value="500">最近 500 行</option>
            </Select>
            <Button type="button" variant="outline" onClick={() => mutateLogs()}>{"刷新日志"}</Button>
            <Button type="button" variant="destructive" onClick={clearLogs} disabled={clearingLogs}>{clearingLogs ? "清空中..." : "清空日志"}</Button>
            <span className="text-xs text-slate-500">{"日志目标："}{logData?.path || runtime?.logPath || "尚未生成日志文件"}</span>
          </div>
          {logData?.lastError ? <div className="rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700">{"最近错误："}{logData.lastError}</div> : null}
          <div className="overflow-hidden rounded-3xl border border-slate-200 bg-slate-950 text-slate-100 shadow-inner">
            <div className="border-b border-slate-800 px-4 py-3 text-xs text-slate-400">{logData?.path || "尚未生成日志文件"}</div>
            <pre className="h-[360px] overflow-auto whitespace-pre-wrap break-all px-4 py-4 text-xs leading-6">{logData?.content || "暂无日志输出"}</pre>
          </div>
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/95 shadow-sm">
        <CardHeader>
          <CardTitle>{"核心节点列表"}</CardTitle>
          <CardDescription>{"每个节点都会自动生成一个本地 SOCKS5 出口，复用你现有的代理池、测试和 Key 绑定体系。批量测试会统一访问 NVIDIA 官方的 /v1/models 接口。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {error ? <div className="rounded-2xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">{"读取核心节点失败，请稍后重试。"}</div> : null}

          <div className="rounded-3xl border border-slate-200 bg-slate-50/80 p-4">
            <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap gap-2">
                <Badge variant="secondary">{`总节点 ${profiles.length}`}</Badge>
                <Badge variant="secondary">{`已选择 ${effectiveSelectedProfileIds.length}`}</Badge>
                <Badge variant="outline">{"测试目标：NVIDIA /v1/models"}</Badge>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button type="button" variant="outline" size="sm" onClick={() => setSortMode((current) => current === "latency" ? "name" : "latency")}>{sortMode === "latency" ? "改为名称排序" : "速度优先排序"}</Button>
                <Button type="button" variant="outline" size="sm" onClick={toggleSelectAllVisible} disabled={profiles.length === 0}>{allVisibleSelected ? "取消全选" : "全选当前列表"}</Button>
                <Button type="button" variant="outline" size="sm" onClick={() => setSelectedProfileIds([])} disabled={effectiveSelectedProfileIds.length === 0}>{"清空选择"}</Button>
                <Button type="button" variant="outline" size="sm" onClick={deleteFailedProfiles} disabled={failedTestedProfileIds.length === 0 || batchTesting || batchDeleting}>{batchDeleting ? "删除中..." : `一键删除失败节点（${failedTestedProfileIds.length}）`}</Button>
                <Button type="button" size="sm" onClick={runBatchTest} disabled={effectiveSelectedProfileIds.length === 0 || batchTesting || batchDeleting}>{batchTesting ? "批量测试中..." : `批量测试已选（${effectiveSelectedProfileIds.length}）`}</Button>
                <Button type="button" variant="destructive" size="sm" onClick={deleteBatchProfiles} disabled={effectiveSelectedProfileIds.length === 0 || batchTesting || batchDeleting}>{batchDeleting ? "批量删除中..." : `批量删除已选（${effectiveSelectedProfileIds.length}）`}</Button>
              </div>
            </div>
            <div className="mt-3 text-sm text-slate-500">{sortMode === "latency" ? `当前按速度优先排序：已启用且测速成功 → 延迟更低 → 最近测试更近。当前失败节点 ${failedTestedProfileIds.length}个。` : `当前按名称排序。当前失败节点 ${failedTestedProfileIds.length}个。`}</div>
          </div>

          {profiles.length === 0 ? (
            <div className="rounded-3xl border border-dashed border-slate-200 px-4 py-12 text-center text-sm text-slate-500">{"当前还没有核心节点。"}</div>
          ) : (
            <div className="h-[720px] overflow-y-auto space-y-4 pr-2">
              {profiles.map((profile) => {
                const isBusy = busyId === profile.id || batchTesting || batchDeleting;
                const isSelected = selectedProfileIdSet.has(profile.id);
                return (
                  <div key={profile.id} className="overflow-hidden rounded-3xl border border-slate-200 bg-white shadow-sm">
                    <div className="flex flex-col gap-3 border-b border-slate-200/80 bg-slate-50/70 px-4 py-4 xl:flex-row xl:items-start xl:justify-between">
                      <div className="space-y-3">
                        <label className="inline-flex items-center gap-2 text-sm text-slate-600">
                          <input type="checkbox" checked={isSelected} onChange={() => toggleProfileSelection(profile.id)} className="h-4 w-4 rounded border-slate-300" />
                          {"选中此节点"}
                        </label>
                        <div className="flex flex-wrap items-center gap-2">
                          <div className="text-lg font-semibold text-slate-900">{profile.name}</div>
                          <Badge variant="outline">{profile.protocol}</Badge>
                          <Badge variant={profile.status === "Disabled" ? "outline" : "default"}>{profile.status === "Disabled" ? "已禁用" : "已启用"}</Badge>
                          <Badge variant="secondary">{`本地 SOCKS ${profile.localPort}`}</Badge>
                          {profile.managedProxyId ? <Badge variant="secondary">{`代理池 ID ${profile.managedProxyId}`}</Badge> : null}
                        </div>
                      </div>
                      <div className="text-xs text-slate-400">{`更新时间：${formatDate(profile.updatedAt)}`}</div>
                    </div>

                    <div className="grid gap-4 px-4 py-4 xl:grid-cols-[minmax(0,1fr)_220px]">
                      <div className="space-y-4">
                        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
                          <MiniInfoCard label={"服务器地址"} value={`${profile.server}:${profile.port}`} />
                          <MiniInfoCard label={"传输协议"} value={profile.transport || "tcp"} />
                          <MiniInfoCard label={"TLS 模式"} value={profile.tlsMode || "none"} />
                          <MiniInfoCard label={"本地出口"} value={profile.localProxyURL} />
                          {profile.sni ? <MiniInfoCard label={"SNI"} value={profile.sni} /> : null}
                          {profile.host ? <MiniInfoCard label={"Host"} value={profile.host} /> : null}
                          {profile.path ? <MiniInfoCard label={"Path"} value={profile.path} /> : null}
                          {profile.serviceName ? <MiniInfoCard label={"ServiceName"} value={profile.serviceName} /> : null}
                        </div>

                        {profile.lastTest ? (
                          <div className={`rounded-3xl border px-4 py-3 text-sm ${profile.lastTest.success ? "border-emerald-200 bg-emerald-50/80 text-emerald-800" : "border-amber-200 bg-amber-50/80 text-amber-800"}`}>
                            <div className="flex flex-wrap items-center gap-3">
                              <span className="font-medium">{`最近测试：${profile.lastTest.summary}`}</span>
                              <span className="text-xs opacity-80">{formatDate(profile.lastTest.testedAt)}</span>
                              {typeof profile.lastTest.responseTime === "number" ? <span className="text-xs opacity-80">{`${profile.lastTest.responseTime} ms`}</span> : null}
                              {profile.lastTest.target ? <span className="break-all text-xs opacity-80">{profile.lastTest.target}</span> : null}
                            </div>
                            {profile.lastTest.message ? <div className="mt-2 break-all whitespace-pre-wrap text-xs opacity-90">{profile.lastTest.message}</div> : null}
                          </div>
                        ) : (
                          <div className="rounded-3xl border border-dashed border-slate-200 bg-slate-50/80 px-4 py-4 text-sm text-slate-500">{"该节点还没有测试记录。"}</div>
                        )}

                        {profile.remarks ? <div className="text-sm text-slate-500">{`备注：${profile.remarks}`}</div> : null}
                      </div>

                      <div className="flex flex-wrap gap-2 xl:flex-col xl:items-stretch">
                        <Button variant="outline" size="sm" className="xl:w-full" onClick={() => startEdit(profile)} disabled={isBusy}>{"编辑"}</Button>
                        <Button variant="outline" size="sm" className="xl:w-full" onClick={() => copyLocalProxy(profile)} disabled={isBusy}>{"复制本地 SOCKS"}</Button>
                        <Button variant="outline" size="sm" className="xl:w-full" onClick={() => testProfile(profile)} disabled={isBusy}>{"测试到 NVIDIA"}</Button>
                        <Button variant="outline" size="sm" className="xl:w-full" onClick={() => toggleStatus(profile)} disabled={isBusy}>{profile.status === "Disabled" ? "启用" : "禁用"}</Button>
                        <Button variant="destructive" size="sm" className="xl:w-full" onClick={() => deleteProfile(profile)} disabled={isBusy}>{"删除"}</Button>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}


function RuntimeMetricCard({ label, value, tone }: { label: string; value: string; tone: "neutral" | "success" }) {
  const toneClassName = tone === "success" ? "border-emerald-200 bg-emerald-50 text-emerald-700" : "border-slate-200 bg-slate-50 text-slate-600";
  return (
    <div className={`rounded-3xl border px-4 py-3 ${toneClassName}`}>
      <div className="text-xs uppercase tracking-[0.12em] opacity-70">{label}</div>
      <div className="mt-2 text-base font-semibold text-slate-900">{value}</div>
    </div>
  );
}

function PathRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-2xl border border-slate-200 bg-white px-3 py-3">
      <div className="text-[11px] font-medium uppercase tracking-[0.12em] text-slate-400">{label}</div>
      <div className="mt-1 break-all text-xs leading-5 text-slate-700">{value}</div>
    </div>
  );
}

function InfoRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-2xl border border-slate-200 bg-white px-3 py-3">
      <span className="text-slate-500">{label}</span>
      <span className="text-right font-medium text-slate-900 break-all">{value}</span>
    </div>
  );
}

function MiniInfoCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-2xl border border-slate-200 bg-slate-50/80 px-3 py-3 text-sm">
      <div className="text-xs uppercase tracking-[0.12em] text-slate-400">{label}</div>
      <div className="mt-1 break-all text-slate-700">{value}</div>
    </div>
  );
}

function formatDate(value: string) {
  return new Date(value).toLocaleString();
}
