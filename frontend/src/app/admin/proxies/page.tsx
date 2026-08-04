"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import useSWR from "swr";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

const fetcher = async (url: string) => {
  const res = await fetch(url);
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(data?.error || "request_failed");
  }
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

interface UpstreamProxy {
  id: number;
  name: string;
  group?: string;
  country?: string;
  source?: string;
  managedBy?: string;
  managedRefId?: number;
  type: string;
  status: string;
  host: string;
  port: number;
  username?: string;
  hasPassword: boolean;
  boundKeyCount: number;
  urlPreview: string;
  lastTest?: ProxyTestRecord | null;
  testHistory?: ProxyTestRecord[];
  createdAt: string;
  updatedAt: string;
}

interface ProxyListResponse {
  proxies: UpstreamProxy[];
}

interface ProxyFormState {
  name: string;
  group: string;
  country: string;
  type: string;
  host: string;
  port: string;
  username: string;
  password: string;
}

interface ProxyTestResult {
  success?: boolean;
  status_code?: number;
  response_time?: number;
  target?: string;
  message?: string;
}

interface ProxyImportSummary {
  mode: string;
  group: string;
  candidateCount: number;
  testedCount: number;
  availableCount: number;
  failedCount: number;
  importedCount: number;
  updatedCount: number;
  matchedManualCount: number;
  sourceErrorCount: number;
  cleanedSlowCount?: number;
  cleanedFailedCount?: number;
  cleanupDeletedCount?: number;
  unboundKeyCount?: number;
}

interface AutoImportFormState {
  mode: string;
  group: string;
  limit: string;
  concurrency: string;
  timeoutSeconds: string;
  retryCount: string;
  cleanupEnabled: boolean;
  cleanupMaxLatencyMs: string;
  cleanupDeleteFailedAutoProxies: boolean;
}

interface ProxyImportTaskRequest {
  mode: string;
  group: string;
  limit: number;
  concurrency: number;
  timeoutSeconds: number;
  retryCount: number;
  cleanupEnabled: boolean;
  cleanupMaxLatencyMs: number;
  cleanupDeleteFailedAutoProxies: boolean;
}

interface ProxyImportTask {
  id?: string;
  status: string;
  phase: string;
  trigger?: string;
  progress: number;
  message?: string;
  error?: string;
  totalSources?: number;
  completedSources?: number;
  candidateCount?: number;
  testedCount?: number;
  availableCount?: number;
  failedCount?: number;
  persistedCount?: number;
  importedCount?: number;
  updatedCount?: number;
  matchedManualCount?: number;
  cleanedSlowCount?: number;
  cleanedFailedCount?: number;
  cleanupDeletedCount?: number;
  unboundKeyCount?: number;
  startedAt?: string;
  finishedAt?: string;
  request: ProxyImportTaskRequest;
  summary?: ProxyImportSummary | null;
}

interface ProxyImportSchedule {
  enabled: boolean;
  times?: string[];
  mode: string;
  group: string;
  limit: number;
  concurrency: number;
  timeoutSeconds: number;
  retryCount: number;
  cleanupEnabled: boolean;
  cleanupMaxLatencyMs: number;
  cleanupDeleteFailedAutoProxies: boolean;
  lastRunAt?: string;
  updatedAt?: string;
}

interface ProxyImportLog {
  taskId: string;
  trigger: string;
  status: string;
  message?: string;
  mode: string;
  group: string;
  limit: number;
  concurrency: number;
  timeoutSeconds: number;
  retryCount: number;
  cleanupEnabled: boolean;
  cleanupMaxLatencyMs: number;
  cleanupDeleteFailedAutoProxies: boolean;
  startedAt?: string;
  finishedAt?: string;
  candidateCount?: number;
  testedCount?: number;
  availableCount?: number;
  failedCount?: number;
  importedCount?: number;
  updatedCount?: number;
  matchedManualCount?: number;
  sourceErrorCount?: number;
  cleanedSlowCount?: number;
  cleanedFailedCount?: number;
  cleanupDeletedCount?: number;
  unboundKeyCount?: number;
}

interface ProxyImportStateResponse {
  task: ProxyImportTask;
  schedule: ProxyImportSchedule;
  logs?: ProxyImportLog[];
  nextRunAt?: string;
}

interface ExternalProxySources {
  httpTxt: string[];
  httpJSON: string[];
  httpHTML: string[];
  socks5Txt: string[];
  socks5JSON: string[];
  socks5HTML: string[];
}

interface ExternalProxySourceCounts {
  httpTxt: number;
  httpJSON: number;
  httpHTML: number;
  socks5Txt: number;
  socks5JSON: number;
  socks5HTML: number;
  total: number;
}

interface ExternalProxySourcesResponse {
  sources: ExternalProxySources;
  builtin: ExternalProxySourceCounts;
  external: ExternalProxySourceCounts;
  effective: ExternalProxySourceCounts;
}

interface ExternalProxySourcesFormState {
  httpTxt: string;
  httpJSON: string;
  httpHTML: string;
  socks5Txt: string;
  socks5JSON: string;
  socks5HTML: string;
}

interface ScheduleFormState extends AutoImportFormState {
  enabled: boolean;
  timesText: string;
}

const emptyForm: ProxyFormState = {
  name: "",
  group: "",
  country: "",
  type: "http",
  host: "",
  port: "",
  username: "",
  password: "",
};

const defaultAutoImportForm: AutoImportFormState = {
  mode: "all",
  group: "自动抓取",
  limit: "800",
  concurrency: "96",
  timeoutSeconds: "4",
  retryCount: "0",
  cleanupEnabled: false,
  cleanupMaxLatencyMs: "3000",
  cleanupDeleteFailedAutoProxies: false,
};

export default function ProxiesPage() {
  const { data, error, mutate } = useSWR<ProxyListResponse>("/api/proxies", fetcher, {
    refreshInterval: 15000,
    revalidateOnFocus: false,
    refreshWhenHidden: false,
  });
  const { data: importMeta, mutate: mutateImportMeta } = useSWR<ProxyImportStateResponse>("/api/proxies/import/free", fetcher, {
    refreshInterval: 5000,
    revalidateOnFocus: false,
    refreshWhenHidden: false,
  });
  const { data: sourceMeta, mutate: mutateSourceMeta } = useSWR<ExternalProxySourcesResponse>("/api/proxies/import/sources", fetcher);

  const [groupFilter, setGroupFilter] = useState<string>("");
  const [countryFilter, setCountryFilter] = useState<string>("");
  const [createForm, setCreateForm] = useState<ProxyFormState>(emptyForm);
  const [editingProxyId, setEditingProxyId] = useState<number | null>(null);
  const [editForm, setEditForm] = useState<ProxyFormState>(emptyForm);
  const [busyProxyId, setBusyProxyId] = useState<number | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [startingImport, setStartingImport] = useState(false);
  const [savingSchedule, setSavingSchedule] = useState(false);
  const [savingSourceConfig, setSavingSourceConfig] = useState(false);
  const [clearingImportLogs, setClearingImportLogs] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<ProxyTestResult | null>(null);
  const [importForm, setImportForm] = useState<AutoImportFormState>(defaultAutoImportForm);
  const [scheduleDraft, setScheduleDraft] = useState<ScheduleFormState | null>(null);
  const [sourceDraft, setSourceDraft] = useState<ExternalProxySourcesFormState | null>(null);
  const [selectedProxyIds, setSelectedProxyIds] = useState<number[]>([]);
  const [batchUpdating, setBatchUpdating] = useState(false);

  const lastTaskIdRef = useRef<string | null>(null);
  const lastTaskStatusRef = useRef<string | null>(null);

  useEffect(() => {
    if (!message && !errorMessage) return;
    const timer = window.setTimeout(() => {
      setMessage(null);
      setErrorMessage(null);
    }, 4000);
    return () => window.clearTimeout(timer);
  }, [message, errorMessage]);

  useEffect(() => {
    const task = importMeta?.task;
    if (!task?.id) {
      lastTaskIdRef.current = task?.id ?? null;
      lastTaskStatusRef.current = task?.status ?? null;
      return;
    }
    const prevTaskId = lastTaskIdRef.current;
    const prevStatus = lastTaskStatusRef.current;
    if (prevTaskId === task.id && prevStatus === "running" && task.status === "succeeded") {
      setMessage(task.message || "自动抓取已完成。");
      void mutate();
    }
    if (prevTaskId === task.id && prevStatus === "running" && task.status === "failed") {
      setErrorMessage(task.error || task.message || "自动抓取失败。");
      void mutate();
    }
    lastTaskIdRef.current = task.id;
    lastTaskStatusRef.current = task.status;
  }, [importMeta?.task, mutate]);

  const proxyGroups = useMemo(() => uniqueSorted((data?.proxies ?? []).map((proxy) => proxy.group ?? "")), [data?.proxies]);
  const proxyCountries = useMemo(() => uniqueSorted((data?.proxies ?? []).map((proxy) => proxy.country ?? "")), [data?.proxies]);

  const proxies = useMemo(() => {
    const rank = (proxy: UpstreamProxy) => {
      if (proxy.status === "Disabled") return 3;
      if (!proxy.lastTest) return 2;
      return proxy.lastTest.success ? 0 : 1;
    };
    const latency = (proxy: UpstreamProxy) => proxy.lastTest?.responseTime ?? Number.MAX_SAFE_INTEGER;
    const testedAt = (proxy: UpstreamProxy) => (proxy.lastTest?.testedAt ? new Date(proxy.lastTest.testedAt).getTime() : 0);
    return [...(data?.proxies ?? [])]
      .filter((proxy) => !groupFilter || (proxy.group ?? "") === groupFilter)
      .filter((proxy) => !countryFilter || (proxy.country ?? "") === countryFilter)
      .sort((a, b) => {
        if (rank(a) !== rank(b)) return rank(a) - rank(b);
        if (latency(a) !== latency(b)) return latency(a) - latency(b);
        if (testedAt(a) !== testedAt(b)) return testedAt(b) - testedAt(a);
        if ((a.country ?? "") !== (b.country ?? "")) return (a.country ?? "").localeCompare(b.country ?? "");
        if ((a.group ?? "") !== (b.group ?? "")) return (a.group ?? "").localeCompare(b.group ?? "");
        return a.name.localeCompare(b.name);
      });
  }, [countryFilter, data?.proxies, groupFilter]);

  const healthyProxies = useMemo(() => proxies.filter((item) => item.lastTest?.success !== false), [proxies]);
  const failedProxies = useMemo(() => proxies.filter((item) => item.lastTest?.success === false), [proxies]);

  const importTask = importMeta?.task;
  const latestImportSummary = importTask?.summary ?? null;
  const importTaskRunning = importTask?.status === "running";
  const importTaskProgress = Math.max(0, Math.min(100, importTask?.progress ?? 0));
  const scheduleForm = scheduleDraft ?? (importMeta?.schedule ? scheduleToForm(importMeta.schedule) : defaultScheduleForm());
  const sourceForm = sourceDraft ?? externalSourcesToForm(sourceMeta?.sources);
  const importLogs = importMeta?.logs ?? [];
  const selectedVisibleCount = proxies.filter((proxy) => selectedProxyIds.includes(proxy.id)).length;
  const allVisibleSelected = proxies.length > 0 && selectedVisibleCount === proxies.length;

  const resetMessages = () => {
    setMessage(null);
    setErrorMessage(null);
    setTestResult(null);
  };


  const validateProxyForm = (form: ProxyFormState) => {
    if (!form.name.trim()) return "代理名称不能为空。";
    if (!form.host.trim()) return "代理主机不能为空。";
    const port = Number(form.port);
    if (!Number.isFinite(port) || port <= 0 || port > 65535) return "代理端口必须在 1-65535 之间。";
    return null;
  };

  const validateImportForm = (form: AutoImportFormState) => {
    const limit = Number(form.limit);
    const concurrency = Number(form.concurrency);
    const timeoutSeconds = Number(form.timeoutSeconds);
    if (!Number.isFinite(limit) || limit <= 0) return "抓取上限必须大于 0。";
    if (!Number.isFinite(concurrency) || concurrency <= 0) return "并发测试数必须大于 0。";
    if (!Number.isFinite(timeoutSeconds) || timeoutSeconds <= 0) return "单个代理超时必须大于 0。";
    return null;
  };

  const buildImportPayload = (form: AutoImportFormState) => ({
    mode: form.mode,
    group: form.group.trim(),
    limit: Number(form.limit),
    concurrency: Number(form.concurrency),
    timeoutSeconds: Number(form.timeoutSeconds),
    retryCount: Number(form.retryCount),
    cleanupEnabled: form.cleanupEnabled,
    cleanupMaxLatencyMs: Number(form.cleanupMaxLatencyMs),
    cleanupDeleteFailedAutoProxies: form.cleanupDeleteFailedAutoProxies,
  });

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    resetMessages();
    const validationError = validateProxyForm(createForm);
    if (validationError) {
      setErrorMessage(validationError);
      return;
    }
    setSubmitting(true);
    try {
      const res = await fetch("/api/proxies", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: createForm.name.trim(),
          group: createForm.group.trim(),
          country: createForm.country.trim(),
          type: createForm.type,
          host: createForm.host.trim(),
          port: Number(createForm.port),
          username: createForm.username.trim(),
          password: createForm.password,
        }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "新增代理失败。");
        return;
      }
      setCreateForm(emptyForm);
      setMessage(payload?.message || "代理已添加。");
      await mutate();
    } finally {
      setSubmitting(false);
    }
  };

  const startEdit = (proxy: UpstreamProxy) => {
    resetMessages();
    setEditingProxyId(proxy.id);
    setEditForm({
      name: proxy.name,
      group: proxy.group ?? "",
      country: proxy.country ?? "",
      type: proxy.type,
      host: proxy.host,
      port: String(proxy.port),
      username: proxy.username ?? "",
      password: "",
    });
  };

  const cancelEdit = () => {
    setEditingProxyId(null);
    setEditForm(emptyForm);
  };

  const toggleStatus = async (proxy: UpstreamProxy) => {
    resetMessages();
    setBusyProxyId(proxy.id);
    try {
      const nextStatus = proxy.status === "Disabled" ? "Enabled" : "Disabled";
      const res = await fetch(`/api/proxies/${proxy.id}/status`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: nextStatus }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "更新代理状态失败。");
        return;
      }
      setMessage(payload?.message || (nextStatus === "Disabled" ? "代理已禁用。" : "代理已启用。"));
      await mutate();
    } finally {
      setBusyProxyId(null);
    }
  };

  const saveEdit = async (id: number) => {
    resetMessages();
    const validationError = validateProxyForm(editForm);
    if (validationError) {
      setErrorMessage(validationError);
      return;
    }
    setBusyProxyId(id);
    try {
      const res = await fetch(`/api/proxies/${id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: editForm.name.trim(),
          group: editForm.group.trim(),
          country: editForm.country.trim(),
          type: editForm.type,
          host: editForm.host.trim(),
          port: Number(editForm.port),
          username: editForm.username.trim(),
          password: editForm.password,
        }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "更新代理失败。");
        return;
      }
      setMessage(payload?.message || "代理已更新。");
      cancelEdit();
      await mutate();
    } finally {
      setBusyProxyId(null);
    }
  };

  const deleteProxy = async (proxy: UpstreamProxy) => {
    resetMessages();
    if (!window.confirm(`确定要删除代理「${proxy.name}」吗？绑定它的上游 key 会自动解绑。`)) return;
    setBusyProxyId(proxy.id);
    try {
      const res = await fetch(`/api/proxies/${proxy.id}`, { method: "DELETE" });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "删除代理失败。");
        return;
      }
      setMessage(payload?.message || "代理已删除。");
      if (editingProxyId === proxy.id) cancelEdit();
      await mutate();
    } finally {
      setBusyProxyId(null);
    }
  };

  const testProxy = async (proxy: UpstreamProxy) => {
    resetMessages();
    setBusyProxyId(proxy.id);
    try {
      const res = await fetch("/api/proxies/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ proxyId: proxy.id }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "测试代理失败。");
        return;
      }
      setTestResult(payload);
      setMessage(payload?.message || "已完成代理测试。");
      await mutate();
    } finally {
      setBusyProxyId(null);
    }
  };

  const handleAutoImport = async () => {
    resetMessages();
    const validationError = validateImportForm(importForm);
    if (validationError) {
      setErrorMessage(validationError);
      return;
    }
    setStartingImport(true);
    try {
      const res = await fetch("/api/proxies/import/free", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(buildImportPayload(importForm)),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "自动抓取代理失败。");
        await mutateImportMeta();
        return;
      }
      setMessage(payload?.message || "后台任务已启动。");
      await mutateImportMeta();
    } finally {
      setStartingImport(false);
    }
  };

  const handleSaveSchedule = async () => {
    resetMessages();
    if (!scheduleForm) return;
    const validationError = validateImportForm(scheduleForm);
    if (validationError) {
      setErrorMessage(validationError);
      return;
    }
    const times = splitScheduleTimes(scheduleForm.timesText);
    if (scheduleForm.enabled && times.length === 0) {
      setErrorMessage("开启定时抓取时，至少填写一个时间。多个时间请用逗号分隔。");
      return;
    }
    setSavingSchedule(true);
    try {
      const res = await fetch("/api/proxies/import/free", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          enabled: scheduleForm.enabled,
          times,
          mode: scheduleForm.mode,
          group: scheduleForm.group.trim(),
          limit: Number(scheduleForm.limit),
          concurrency: Number(scheduleForm.concurrency),
          timeoutSeconds: Number(scheduleForm.timeoutSeconds),
          retryCount: Number(scheduleForm.retryCount),
          cleanupEnabled: scheduleForm.cleanupEnabled,
          cleanupMaxLatencyMs: Number(scheduleForm.cleanupMaxLatencyMs),
          cleanupDeleteFailedAutoProxies: scheduleForm.cleanupDeleteFailedAutoProxies,
        }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "保存定时抓取配置失败。");
        return;
      }
      setMessage(payload?.message || "定时抓取配置已保存。");
      if (payload?.schedule) {
        setScheduleDraft(scheduleToForm(payload.schedule));
      }
      await mutateImportMeta();
    } finally {
      setSavingSchedule(false);
    }
  };

  const handleClearImportLogs = async () => {
    resetMessages();
    if (importLogs.length === 0) return;
    if (!window.confirm(`确定要清空最近 ${importLogs.length} 条执行日志吗？`)) return;
    setClearingImportLogs(true);
    try {
      const res = await fetch("/api/proxies/import/free/logs", {
        method: "DELETE",
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "清空最近执行日志失败。");
        return;
      }
      setMessage(payload?.message || "最近执行日志已清空。");
      await mutateImportMeta();
    } finally {
      setClearingImportLogs(false);
    }
  };

  const handleSaveExternalSources = async () => {
    resetMessages();
    setSavingSourceConfig(true);
    try {
      const payloadBody = buildExternalSourcesPayload(sourceForm);
      const res = await fetch("/api/proxies/import/sources", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payloadBody),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "保存外置代理源配置失败。");
        return;
      }
      const nextConfig = payload?.config ?? payload;
      if (nextConfig?.sources) {
        setSourceDraft(externalSourcesToForm(nextConfig.sources));
      }
      setMessage(payload?.message || "外置代理源配置更新成功。");
      await mutateSourceMeta();
    } finally {
      setSavingSourceConfig(false);
    }
  };

  const toggleProxySelection = (proxyId: number) => {
    setSelectedProxyIds((current) => current.includes(proxyId) ? current.filter((id) => id !== proxyId) : [...current, proxyId]);
  };

  const toggleVisibleSelection = () => {
    if (allVisibleSelected) {
      setSelectedProxyIds((current) => current.filter((id) => !proxies.some((proxy) => proxy.id === id)));
      return;
    }
    const visibleIds = proxies.map((proxy) => proxy.id);
    setSelectedProxyIds((current) => Array.from(new Set([...current, ...visibleIds])));
  };

  const handleBatchDelete = async () => {
    resetMessages();
    if (selectedProxyIds.length === 0) {
      setErrorMessage("请先选择至少一个代理。");
      return;
    }
    if (!window.confirm(`确定要批量删除 ${selectedProxyIds.length} 个代理吗？相关 key 会自动解绑。`)) return;
    setBatchUpdating(true);
    try {
      const res = await fetch("/api/proxies/batch", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: selectedProxyIds }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "批量删除代理失败。");
        return;
      }
      setMessage(payload?.message || "批量删除完成。");
      setSelectedProxyIds([]);
      await mutate();
    } finally {
      setBatchUpdating(false);
    }
  };

  const handleBatchCopy = async () => {
    resetMessages();
    if (selectedProxyIds.length === 0) {
      setErrorMessage("请先选择至少一个代理。");
      return;
    }
    try {
      const res = await fetch("/api/proxies/export", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: selectedProxyIds }),
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setErrorMessage(payload?.error || "批量复制代理失败。");
        return;
      }
      const content = String(payload?.content || "").trim();
      if (!content) {
        setErrorMessage("没有可复制的代理内容。");
        return;
      }
      await navigator.clipboard.writeText(content);
      setMessage(`已复制 ${payload?.count ?? selectedProxyIds.length} 条代理到剪贴板。`);
    } catch {
      setErrorMessage("批量复制代理失败。");
    }
  };

  return (
    <div className="space-y-6">
      <section className="rounded-[30px] border border-slate-200/70 bg-white/90 p-8 shadow-sm">
        <div className="max-w-3xl">
          <div className="text-xs uppercase tracking-[0.24em] text-slate-400">代理池</div>
          <h1 className="mt-3 text-3xl font-semibold tracking-tight text-slate-900">管理上游代理池</h1>
          <p className="mt-3 text-sm leading-7 text-slate-500">这里可以保存 HTTP / HTTPS / SOCKS5 代理，并把它们绑定到具体的 NVIDIA 上游 key。未绑定代理的 key 会继续走系统设置中的全局上游代理。</p>
        </div>
      </section>

      {(message || errorMessage) ? (
        <div className={`rounded-2xl border px-5 py-4 text-sm ${errorMessage ? "border-rose-200 bg-rose-50 text-rose-700" : "border-emerald-200 bg-emerald-50 text-emerald-700"}`}>
          {errorMessage || message}
        </div>
      ) : null}
      
      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>{"Xray 节点入口"}</CardTitle>
          <CardDescription>{"从专用 Xray 页面添加 vless / vmess / shadowsocks / trojan / socks / http 节点。保存后会自动映射回当前代理池。"}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
          <div className="grid gap-2 text-sm text-slate-600">
            <div>{"功能：分享链接解析、订阅导入、日志查看、本地 SOCKS5 映射。"}</div>
            <div>{"如果你要添加 vless / vmess 节点，请点击右侧按钮进入 Xray 节点页。"}</div>
          </div>
          <Link href="/admin/core" className="inline-flex items-center rounded-2xl bg-slate-900 px-4 py-2 text-sm font-medium text-white shadow-sm transition hover:bg-slate-800">{"打开 Xray 节点页"}</Link>
        </CardContent>
      </Card>



      {testResult ? (
        <div className={`rounded-2xl border px-5 py-4 text-sm ${testResult.success ? "border-emerald-200 bg-emerald-50 text-emerald-700" : "border-amber-200 bg-amber-50 text-amber-800"}`}>
          <div className="font-medium">最近一次手动测试</div>
          <div className="mt-2">{testResult.message || "已完成测试。"}</div>
          <div className="mt-2 text-xs opacity-80">
            {testResult.target ? <span>目标：{testResult.target}</span> : null}
            {typeof testResult.status_code === "number" ? <span className="ml-4">HTTP {testResult.status_code}</span> : null}
            {typeof testResult.response_time === "number" ? <span className="ml-4">耗时 {testResult.response_time} ms</span> : null}
          </div>
        </div>
      ) : null}

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>自动抓取免费代理</CardTitle>
          <CardDescription>自动抓取改为后台异步执行，可实时查看进度。任务会先抓取候选源，再逐个测速，只保留当前可用的代理节点。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">

          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-6">
            <Select value={importForm.mode} onChange={(e) => setImportForm({ ...importForm, mode: e.target.value })}>
              <option value="all">HTTP + SOCKS5</option>
              <option value="http">{"仅 HTTP"}</option>
              <option value="socks5">{"仅 SOCKS5"}</option>
            </Select>
            <Input value={importForm.group} onChange={(e) => setImportForm({ ...importForm, group: e.target.value })} placeholder={"自动分组名称"} />
            <Input value={importForm.limit} onChange={(e) => setImportForm({ ...importForm, limit: e.target.value })} placeholder={"抓取上限"} />
            <Input value={importForm.concurrency} onChange={(e) => setImportForm({ ...importForm, concurrency: e.target.value })} placeholder={"并发测试数"} />
            <Input value={importForm.timeoutSeconds} onChange={(e) => setImportForm({ ...importForm, timeoutSeconds: e.target.value })} placeholder={"单个超时（秒）"} />
            <Input value={importForm.retryCount} onChange={(e) => setImportForm({ ...importForm, retryCount: e.target.value })} placeholder={"失败重试次数"} />
          </div>

          <div className="rounded-2xl border border-slate-200 bg-white p-4">
            <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
              <div>
                <div className="text-sm font-semibold text-slate-900">{"自动清理策略"}</div>
                <div className="mt-1 text-xs leading-6 text-slate-500">{"抓取完成后，可以自动删除慢代理或失效的自动代理，并自动解绑关联 key。"}</div>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-sm text-slate-500">{importForm.cleanupEnabled ? "已开启" : "已关闭"}</span>
                <Switch checked={importForm.cleanupEnabled} onCheckedChange={(checked) => setImportForm({ ...importForm, cleanupEnabled: checked })} />
              </div>
            </div>
            <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-3">
              <Input value={importForm.cleanupMaxLatencyMs} onChange={(e) => setImportForm({ ...importForm, cleanupMaxLatencyMs: e.target.value })} placeholder={"慢代理阈值（ms）"} />
              <div className="flex items-center gap-3 rounded-2xl border border-slate-200 px-4 py-2 text-sm text-slate-600">
                <span>{"删除失效自动代理"}</span>
                <Switch checked={importForm.cleanupDeleteFailedAutoProxies} onCheckedChange={(checked) => setImportForm({ ...importForm, cleanupDeleteFailedAutoProxies: checked })} />
              </div>
              <div className="text-xs leading-6 text-slate-500">{"只会清理自动抓取生成的代理，不会影响手动代理。"}</div>
            </div>
          </div>

          <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
            <div className="text-xs leading-6 text-slate-500">手动点击后任务会在后台执行；页面可关闭，重新打开后仍能看到当前任务状态。默认测速结果越快的代理会排在越前面。</div>
            <Button type="button" onClick={handleAutoImport} disabled={startingImport || importTaskRunning}>{startingImport ? "启动中..." : importTaskRunning ? "任务执行中..." : "后台抓取并测试"}</Button>
          </div>

          <div className="rounded-2xl border border-slate-200 bg-slate-50/80 p-4">
            <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <div className="text-sm font-semibold text-slate-900">当前后台任务</div>
                  <Badge variant={taskStatusVariant(importTask?.status)}>{formatTaskStatus(importTask?.status)}</Badge>
                  {importTask?.trigger ? <Badge variant="outline">{formatTaskTrigger(importTask.trigger)}</Badge> : null}
                  {importTask?.phase ? <Badge variant="secondary">{formatTaskPhase(importTask.phase)}</Badge> : null}
                </div>
                <div className="mt-2 text-sm text-slate-600">{importTask?.message || "暂无后台任务。"}</div>
              </div>
              <div className="text-right text-xs text-slate-500">
                {importTask?.startedAt ? <div>开始：{formatDate(importTask.startedAt)}</div> : null}
                {importTask?.finishedAt ? <div>结束：{formatDate(importTask.finishedAt)}</div> : null}
              </div>
            </div>
            <div className="mt-4 h-3 overflow-hidden rounded-full bg-slate-200">
              <div className={`h-full rounded-full transition-all ${importTask?.status === "failed" ? "bg-rose-500" : importTaskRunning ? "bg-sky-500" : "bg-emerald-500"}`} style={{ width: `${importTaskProgress}%` }} />
            </div>

            <div className="mt-3 grid gap-2 text-xs text-slate-500 md:grid-cols-3 xl:grid-cols-6">
              <div>{`进度：${importTaskProgress}%`}</div>
              <div>{`代理源：${importTask?.completedSources ?? 0} / ${importTask?.totalSources ?? 0}`}</div>
              <div>{`候选：${importTask?.candidateCount ?? 0}`}</div>
              <div>{`测速：${importTask?.testedCount ?? 0}`}</div>
              <div>{`可用：${importTask?.availableCount ?? 0}`}</div>
              <div>{`新增/更新：${importTask?.importedCount ?? 0} / ${importTask?.updatedCount ?? 0}`}</div>
              <div>{`清理/解绑：${importTask?.cleanupDeletedCount ?? 0} / ${importTask?.unboundKeyCount ?? 0}`}</div>
            </div>
          </div>

                    {latestImportSummary && (
            <div className="rounded-2xl border border-sky-200 bg-sky-50 px-5 py-4 text-sm text-sky-900">
              <div className="font-medium">{"最近一次自动抓取结果"}</div>
              <div className="mt-3 grid gap-2 text-xs md:grid-cols-3 xl:grid-cols-5">
                <div>{`模式：${formatImportMode(latestImportSummary.mode)}`}</div>
                <div>{`候选：${latestImportSummary.candidateCount}`}</div>
                <div>{`测试：${latestImportSummary.testedCount}`}</div>
                <div>{`可用：${latestImportSummary.availableCount}`}</div>
                <div>{`失败：${latestImportSummary.failedCount}`}</div>
                <div>{`新增：${latestImportSummary.importedCount}`}</div>
                <div>{`更新自动代理：${latestImportSummary.updatedCount}`}</div>
                <div>{`清理慢代理：${latestImportSummary.cleanedSlowCount ?? 0}`}</div>
                <div>{`清理失效代理：${latestImportSummary.cleanedFailedCount ?? 0}`}</div>
                <div>{`总清理数：${latestImportSummary.cleanupDeletedCount ?? 0}`}</div>
                <div>{`解绑 key：${latestImportSummary.unboundKeyCount ?? 0}`}</div>
              </div>
            </div>
          )}

          {importLogs.length > 0 && (
            <div className="rounded-2xl border border-slate-200 bg-white p-5">
              <div className="flex items-center justify-between gap-3">
                <div className="text-base font-semibold text-slate-900">{"最近执行日志"}</div>
                <Button variant="outline" size="sm" type="button" onClick={handleClearImportLogs} disabled={clearingImportLogs}>
                  {clearingImportLogs ? "清空中..." : "清空日志"}
                </Button>
              </div>
              <div className="mt-4 space-y-3">
                {importLogs.slice(0, 8).map((log) => (
                  <div key={log.taskId} className="rounded-2xl border border-slate-200 bg-slate-50/80 p-4">
                    <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant={taskStatusVariant(log.status)}>{formatTaskStatus(log.status)}</Badge>
                        <Badge variant="outline">{formatTaskTrigger(log.trigger)}</Badge>
                        <Badge variant="secondary">{formatImportMode(log.mode)}</Badge>
                        {log.group ? <Badge variant="outline">{log.group}</Badge> : null}
                      </div>
                      <div className="text-xs text-slate-500">
                        {log.startedAt ? `开始 ${formatDate(log.startedAt)}` : ""}
                        {log.finishedAt ? ` · 结束 ${formatDate(log.finishedAt)}` : ""}
                      </div>
                    </div>
                    <div className="mt-2 text-sm text-slate-700">{log.message || "-"}</div>
                    <div className="mt-3 grid gap-2 text-xs text-slate-500 md:grid-cols-3 xl:grid-cols-6">
                      <div>{`候选：${log.candidateCount ?? 0}`}</div>
                      <div>{`测速：${log.testedCount ?? 0}`}</div>
                      <div>{`可用：${log.availableCount ?? 0}`}</div>
                      <div>{`新增/更新：${log.importedCount ?? 0} / ${log.updatedCount ?? 0}`}</div>
                      <div>{`重试：${log.retryCount ?? 0}`}</div>
                      <div>{`清理：${log.cleanupDeletedCount ?? 0}`}</div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="rounded-2xl border border-slate-200 bg-white p-5">
            <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
              <div>
                <div className="text-base font-semibold text-slate-900">{"外置代理源配置"}</div>
                <div className="mt-1 text-sm text-slate-500">{"在项目内置代理源的基础上额外追加 URL，一行一个地址。保存后会和内置源一起参与抓取。"}</div>
              </div>
              <div className="flex items-center gap-3">
                <Button type="button" onClick={handleSaveExternalSources} disabled={savingSourceConfig}>{savingSourceConfig ? "保存中..." : "保存外置源"}</Button>
              </div>
            </div>

            <div className="mt-4 grid gap-3 text-xs text-slate-500 md:grid-cols-3">
              <div>{`内置源总数：${sourceMeta?.builtin?.total ?? 0}`}</div>
              <div>{`外置源总数：${sourceMeta?.external?.total ?? 0}`}</div>
              <div>{`生效源总数：${sourceMeta?.effective?.total ?? 0}`}</div>
            </div>

            <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-3">
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">HTTP TXT</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/http.txt"} value={sourceForm.httpTxt} onChange={(e) => setSourceDraft({ ...sourceForm, httpTxt: e.target.value })} />
              </label>
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">HTTP JSON</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/http.json"} value={sourceForm.httpJSON} onChange={(e) => setSourceDraft({ ...sourceForm, httpJSON: e.target.value })} />
              </label>
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">HTTP HTML</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/list.html"} value={sourceForm.httpHTML} onChange={(e) => setSourceDraft({ ...sourceForm, httpHTML: e.target.value })} />
              </label>
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">SOCKS5 TXT</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/socks5.txt"} value={sourceForm.socks5Txt} onChange={(e) => setSourceDraft({ ...sourceForm, socks5Txt: e.target.value })} />
              </label>
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">SOCKS5 JSON</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/socks5.json"} value={sourceForm.socks5JSON} onChange={(e) => setSourceDraft({ ...sourceForm, socks5JSON: e.target.value })} />
              </label>
              <label className="block text-sm text-slate-700">
                <div className="mb-2 font-medium">SOCKS5 HTML</div>
                <textarea className="min-h-32 w-full rounded-2xl border border-slate-200 px-3 py-2 text-sm text-slate-900 outline-none ring-0 placeholder:text-slate-400" placeholder={"https://example.com/socks5.html"} value={sourceForm.socks5HTML} onChange={(e) => setSourceDraft({ ...sourceForm, socks5HTML: e.target.value })} />
              </label>
            </div>
          </div>

          <div className="rounded-2xl border border-slate-200 bg-white p-5">
            <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
              <div>
                <div className="text-base font-semibold text-slate-900">定时自动获取代理</div>
                <div className="mt-1 text-sm text-slate-500">支持开启/关闭、手动输入多个时间点。时间使用当前服务器本地时间，多个时间请用逗号分隔，例如：09:00, 14:30, 23:10。</div>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-sm text-slate-500">{scheduleForm?.enabled ? "已开启" : "已关闭"}</span>
                <Switch checked={scheduleForm?.enabled ?? false} onCheckedChange={(checked) => setScheduleDraft((current) => ({ ...(current ?? defaultScheduleForm()), enabled: checked }))} />
              </div>
            </div>

            <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-6">
              <Input value={scheduleForm.timesText} onChange={(e) => setScheduleDraft({ ...scheduleForm, timesText: e.target.value })} placeholder={"时间，如 09:00, 18:30"} className="xl:col-span-2" />
              <Select value={scheduleForm.mode} onChange={(e) => setScheduleDraft({ ...scheduleForm, mode: e.target.value })}>
                <option value="all">HTTP + SOCKS5</option>
                <option value="http">{"仅 HTTP"}</option>
                <option value="socks5">{"仅 SOCKS5"}</option>
              </Select>
              <Input value={scheduleForm.group} onChange={(e) => setScheduleDraft({ ...scheduleForm, group: e.target.value })} placeholder={"定时抓取分组"} />
              <Input value={scheduleForm.limit} onChange={(e) => setScheduleDraft({ ...scheduleForm, limit: e.target.value })} placeholder={"抓取上限"} />
              <Input value={scheduleForm.retryCount} onChange={(e) => setScheduleDraft({ ...scheduleForm, retryCount: e.target.value })} placeholder={"失败重试次数"} />
            </div>

            <div className="mt-4 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <Input value={scheduleForm.concurrency} onChange={(e) => setScheduleDraft({ ...scheduleForm, concurrency: e.target.value })} placeholder={"并发测试数"} />
              <Input value={scheduleForm.timeoutSeconds} onChange={(e) => setScheduleDraft({ ...scheduleForm, timeoutSeconds: e.target.value })} placeholder={"单个超时（秒）"} />
              <Input value={scheduleForm.cleanupMaxLatencyMs} onChange={(e) => setScheduleDraft({ ...scheduleForm, cleanupMaxLatencyMs: e.target.value })} placeholder={"慢代理阈值（ms）"} />
              <div className="flex items-center gap-3">
                <Button type="button" onClick={handleSaveSchedule} disabled={savingSchedule}>{savingSchedule ? "保存中..." : "保存定时配置"}</Button>
              </div>
            </div>

            <div className="mt-4 grid gap-4 md:grid-cols-2">
              <div className="flex items-center gap-3 rounded-2xl border border-slate-200 px-4 py-3 text-sm text-slate-600">
                <span>{"启用导入后自动清理"}</span>
                <Switch checked={scheduleForm.cleanupEnabled} onCheckedChange={(checked) => setScheduleDraft({ ...scheduleForm, cleanupEnabled: checked })} />
              </div>
              <div className="flex items-center gap-3 rounded-2xl border border-slate-200 px-4 py-3 text-sm text-slate-600">
                <span>{"删除失效自动代理"}</span>
                <Switch checked={scheduleForm.cleanupDeleteFailedAutoProxies} onCheckedChange={(checked) => setScheduleDraft({ ...scheduleForm, cleanupDeleteFailedAutoProxies: checked })} />
              </div>
            </div>

            <div className="mt-4 grid gap-2 text-xs text-slate-500 md:grid-cols-3">
              <div>下次执行：{importMeta?.nextRunAt ? formatDate(importMeta.nextRunAt) : "未安排"}</div>
              <div>上次执行：{importMeta?.schedule?.lastRunAt ? formatDate(importMeta.schedule.lastRunAt) : "暂无"}</div>
              <div>最近保存：{importMeta?.schedule?.updatedAt ? formatDate(importMeta.schedule.updatedAt) : "暂无"}</div>
            </div>
          </div>

        </CardContent>
      </Card>
      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>新增代理</CardTitle>
          <CardDescription>新增一个可复用的上游代理节点，供全局设置之外的单 key 定向出站使用。</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-4 md:grid-cols-2 xl:grid-cols-4" onSubmit={handleCreate}>
            <Input value={createForm.name} onChange={(e) => setCreateForm({ ...createForm, name: e.target.value })} placeholder="代理名称" />
            <Input value={createForm.group} onChange={(e) => setCreateForm({ ...createForm, group: e.target.value })} placeholder="分组（可选，例如：新加坡）" />
            <Input value={createForm.country} onChange={(e) => setCreateForm({ ...createForm, country: e.target.value })} placeholder="国家/地区（可选，例如：US、新加坡）" />
            <Select value={createForm.type} onChange={(e) => setCreateForm({ ...createForm, type: e.target.value })}>
              <option value="http">http</option>
              <option value="https">https</option>
              <option value="socks5">socks5</option>
              <option value="socks5h">socks5</option>
            </Select>
            <Input value={createForm.host} onChange={(e) => setCreateForm({ ...createForm, host: e.target.value })} placeholder="主机 / IP" />
            <Input value={createForm.port} onChange={(e) => setCreateForm({ ...createForm, port: e.target.value })} placeholder="端口" />
            <Input value={createForm.username} onChange={(e) => setCreateForm({ ...createForm, username: e.target.value })} placeholder="用户名（可选）" />
            <Input type="password" value={createForm.password} onChange={(e) => setCreateForm({ ...createForm, password: e.target.value })} placeholder="密码（可选）" />
            <div className="md:col-span-2 xl:col-span-4">
              <Button type="submit" disabled={submitting}>{submitting ? "保存中..." : "添加代理"}</Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>代理列表</CardTitle>
          <CardDescription>支持按分组、国家/地区过滤；默认把测速成功且更快的代理排在前面。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-3">
            <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
              <div className="text-sm text-slate-500">排序规则：已启用且测速成功 → 响应更快 → 最近测试时间更新。也就是“最快的可用代理”默认在最上面。</div>
              <div className="grid w-full gap-3 md:grid-cols-2 xl:w-[32rem]">
                <Select value={groupFilter} onChange={(e) => setGroupFilter(e.target.value)}>
                  <option value="">全部分组</option>
                  {proxyGroups.map((group) => (
                    <option key={group} value={group}>{group}</option>
                  ))}
                </Select>
                <Select value={countryFilter} onChange={(e) => setCountryFilter(e.target.value)}>
                  <option value="">全部国家/地区</option>
                  {proxyCountries.map((country) => (
                    <option key={country} value={country}>{country}</option>
                  ))}
                </Select>
              </div>
            </div>
            <div className="flex flex-col gap-3 rounded-2xl border border-slate-200 bg-slate-50/80 p-4 md:flex-row md:items-center md:justify-between">
              <div className="text-sm text-slate-600">已选择 {selectedProxyIds.length} 个代理，当前筛选结果命中 {selectedVisibleCount} / {proxies.length} 个。</div>
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" size="sm" type="button" onClick={toggleVisibleSelection}>{allVisibleSelected ? "取消当前筛选全选" : "全选当前筛选结果"}</Button>
                <Button variant="outline" size="sm" type="button" onClick={() => setSelectedProxyIds([])} disabled={selectedProxyIds.length === 0}>清空选择</Button>
                <Button variant="outline" size="sm" type="button" onClick={handleBatchCopy} disabled={selectedProxyIds.length === 0 || batchUpdating}>{batchUpdating ? "处理中..." : "批量复制"}</Button>
                <Button variant="outline" size="sm" type="button" onClick={handleBatchDelete} disabled={selectedProxyIds.length === 0 || batchUpdating}>{batchUpdating ? "处理中..." : "批量删除"}</Button>
              </div>
            </div>
          </div>
          {error ? <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">读取代理列表失败，请稍后重试。</div> : null}
          {healthyProxies.length === 0 ? (
            <div className="rounded-xl border border-dashed border-slate-200 px-4 py-10 text-center text-sm text-slate-500">当前筛选条件下没有代理。</div>
          ) : (<div className="max-h-[720px] overflow-y-auto pr-2 space-y-4">{healthyProxies.map((proxy) => {
            const isEditing = editingProxyId === proxy.id;
            const isBusy = busyProxyId === proxy.id;
            return (
              <div key={proxy.id} className="rounded-2xl border border-slate-200 bg-slate-50/80 p-4">
                <div className="flex items-center justify-between gap-3 border-b border-slate-200/80 pb-3">
                  <label className="inline-flex items-center gap-2 text-sm text-slate-600">
                    <input type="checkbox" checked={selectedProxyIds.includes(proxy.id)} onChange={() => toggleProxySelection(proxy.id)} className="h-4 w-4 rounded border-slate-300" />
                    选中该代理
                  </label>
                  <span className="text-xs text-slate-400">批量操作不会影响手动编辑内容</span>
                </div>
                <div className="mt-4 flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
                  <div className="space-y-4">
                    <div>
                      <div className="flex flex-wrap items-center gap-3">
                        <div className="text-lg font-semibold text-slate-900">{proxy.name}</div>
                        <Badge variant="outline">{formatProxyType(proxy.type)}</Badge>
                        <Badge variant="outline">{proxy.source === "auto" ? "自动抓取" : "手动添加"}</Badge>
                        <Badge variant={proxy.status === "Disabled" ? "outline" : "default"}>{proxy.status === "Disabled" ? "已禁用" : "已启用"}</Badge>
                        {proxy.country?.trim() ? <Badge variant="secondary">{proxy.country}</Badge> : null}
                        {typeof proxy.lastTest?.responseTime === "number" ? <Badge variant="secondary">{proxy.lastTest.responseTime} ms</Badge> : null}
                        {proxy.boundKeyCount > 0 ? <Badge variant="default">绑定 {proxy.boundKeyCount} 个 key</Badge> : <Badge variant="outline">未绑定 key</Badge>}
                        <span className="rounded-full border border-slate-200 bg-white px-3 py-1 text-xs text-slate-500">#{String(proxy.id).padStart(4, "0")}</span>
                      </div>
                      <div className="mt-3 grid gap-2 text-sm text-slate-500 md:grid-cols-2 xl:grid-cols-5">
                        <div>分组：{proxy.group?.trim() ? proxy.group : "未分组"}</div>
                        <div>国家/地区：{proxy.country?.trim() ? proxy.country : "未设置"}</div>
                        <div>地址：{proxy.host}:{proxy.port}</div>
                        <div>认证：{proxy.hasPassword ? "已设置" : "无密码"}</div>
                        <div>配置更新时间：{formatDate(proxy.updatedAt)}</div>
                      </div>
                    </div>

                    {proxy.lastTest ? (
                      <div className={`rounded-2xl border px-4 py-3 text-sm ${proxy.lastTest.success ? "border-emerald-200 bg-emerald-50/70 text-emerald-800" : "border-amber-200 bg-amber-50/70 text-amber-800"}`}>
                        <div className="flex flex-wrap items-center gap-3">
                          <span className="font-medium">最近测速：{proxy.lastTest.summary}</span>
                          <span className="text-xs opacity-80">{formatDate(proxy.lastTest.testedAt)}</span>
                          {typeof proxy.lastTest.statusCode === "number" ? <span className="text-xs opacity-80">HTTP {proxy.lastTest.statusCode}</span> : null}
                          {typeof proxy.lastTest.responseTime === "number" ? <span className="text-xs opacity-80">耗时 {proxy.lastTest.responseTime} ms</span> : null}
                          {proxy.lastTest.target ? <span className="break-all text-xs opacity-80">目标 {proxy.lastTest.target}</span> : null}
                        </div>
                        {proxy.lastTest.message ? <div className="mt-2 text-xs opacity-90">{proxy.lastTest.message}</div> : null}
                      </div>
                    ) : (
                      <div className="rounded-2xl border border-dashed border-slate-200 bg-white/70 px-4 py-3 text-sm text-slate-500">还没有测试记录。</div>
                    )}

                    {(proxy.testHistory?.length ?? 0) > 0 ? (
                      <div className="rounded-2xl border border-slate-200 bg-white/70 px-4 py-3">
                        <div className="text-xs uppercase tracking-[0.2em] text-slate-400">测速历史</div>
                        <div className="mt-3 space-y-2 text-sm text-slate-700">
                          {proxy.testHistory!.slice(0, 5).map((item, index) => (
                            <div key={`${proxy.id}-${item.testedAt}-${index}`} className="flex flex-col gap-1 rounded-xl border border-slate-200 bg-slate-50 px-3 py-2 md:flex-row md:items-center md:justify-between">
                              <div className="flex flex-wrap items-center gap-3">
                                <span className={`inline-flex rounded-full px-2.5 py-1 text-xs ${item.success ? "bg-emerald-100 text-emerald-700" : "bg-amber-100 text-amber-700"}`}>{item.summary}</span>
                                <span className="text-xs text-slate-500">{formatDate(item.testedAt)}</span>
                              </div>
                              <div className="text-xs text-slate-500">{typeof item.responseTime === "number" ? `${item.responseTime} ms` : "-"}</div>
                            </div>
                          ))}
                        </div>
                      </div>
                    ) : null}
                  </div>

                  <div className="flex flex-wrap gap-2 xl:max-w-sm xl:justify-end">
                    <Button variant="outline" size="sm" onClick={() => (isEditing ? cancelEdit() : startEdit(proxy))} disabled={isBusy}>{isEditing ? "收起编辑" : "编辑"}</Button>
                    <Button variant="outline" size="sm" onClick={() => toggleStatus(proxy)} disabled={isBusy}>{proxy.status === "Disabled" ? "启用" : "禁用"}</Button>
                    <Button variant="outline" size="sm" onClick={() => testProxy(proxy)} disabled={isBusy}>测试</Button>
                    <Button variant="destructive" size="sm" onClick={() => deleteProxy(proxy)} disabled={isBusy}>删除</Button>
                  </div>
                </div>
                {isEditing ? (
                  <div className="mt-4 grid gap-3 rounded-2xl border border-slate-200 bg-white p-4 md:grid-cols-4">
                    <Input value={editForm.name} onChange={(e) => setEditForm({ ...editForm, name: e.target.value })} placeholder="代理名称" />
                    <Input value={editForm.group} onChange={(e) => setEditForm({ ...editForm, group: e.target.value })} placeholder="分组（可选）" />
                    <Input value={editForm.country} onChange={(e) => setEditForm({ ...editForm, country: e.target.value })} placeholder="国家/地区（可选）" />
                    <Select value={editForm.type} onChange={(e) => setEditForm({ ...editForm, type: e.target.value })}>
                      <option value="http">http</option>
                      <option value="https">https</option>
                      <option value="socks5">socks5</option>
                      <option value="socks5h">socks5</option>
                    </Select>
                    <Input value={editForm.host} onChange={(e) => setEditForm({ ...editForm, host: e.target.value })} placeholder="主机 / IP" />
                    <Input value={editForm.port} onChange={(e) => setEditForm({ ...editForm, port: e.target.value })} placeholder="端口" />
                    <Input value={editForm.username} onChange={(e) => setEditForm({ ...editForm, username: e.target.value })} placeholder="用户名（可选）" />
                    <Input type="password" value={editForm.password} onChange={(e) => setEditForm({ ...editForm, password: e.target.value })} placeholder="留空表示保留当前密码" />
                    <div className="md:col-span-4 flex gap-3">
                      <Button size="sm" onClick={() => saveEdit(proxy.id)} disabled={isBusy}>保存</Button>
                      <Button variant="outline" size="sm" onClick={cancelEdit}>取消</Button>
                    </div>
                  </div>
                ) : null}
              </div>
            );
          })}</div>)}
          <div className="rounded-2xl border border-slate-200 bg-slate-50/70 p-4">
            <div className="mb-3 text-sm font-medium text-slate-800">{"失败 / 失效代理"}</div>
            <div className="max-h-[420px] overflow-y-auto pr-2 space-y-3">
              {failedProxies.length === 0 ? (
                <div className="rounded-xl border border-dashed border-slate-200 bg-white px-4 py-8 text-center text-sm text-slate-500">{"当前没有失败代理记录。"}</div>
              ) : failedProxies.map((proxy) => (
                <div key={`failed-${proxy.id}`} className="rounded-2xl border border-amber-200 bg-white px-4 py-3">
                  <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                    <div className="flex flex-wrap items-center gap-2">
                      <div className="font-medium text-slate-900">{proxy.name}</div>
                      <Badge variant="outline">{formatProxyType(proxy.type)}</Badge>
                      <Badge variant="outline">{proxy.country?.trim() ? proxy.country : "国家未知"}</Badge>
                      <Badge variant="destructive">{proxy.status === "Disabled" ? "已禁用" : "测试失败"}</Badge>
                    </div>
                    <div className="text-xs text-slate-500">{proxy.lastTest?.testedAt ? formatDate(proxy.lastTest.testedAt) : "暂无时间"}</div>
                  </div>
                  <div className="mt-2 grid gap-2 text-sm text-slate-500 md:grid-cols-2 xl:grid-cols-4">
                    <div>{`分组：${proxy.group?.trim() ? proxy.group : "未分组"}`}</div>
                    <div>{`地址：${proxy.host}:${proxy.port}`}</div>
                    <div>{`响应：${typeof proxy.lastTest?.responseTime === "number" ? `${proxy.lastTest.responseTime} ms` : "-"}`}</div>
                    <div>{`状态码：${typeof proxy.lastTest?.statusCode === "number" ? `HTTP ${proxy.lastTest.statusCode}` : "-"}`}</div>
                  </div>
                  <div className="mt-2 text-sm text-amber-700">{proxy.lastTest?.message || "最近一次测试失败。"}</div>
                </div>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function uniqueSorted(values: string[]) {
  const seen = new Set<string>();
  for (const value of values) {
    const trimmed = value.trim();
    if (!trimmed) continue;
    seen.add(trimmed);
  }
  return Array.from(seen).sort((a, b) => a.localeCompare(b));
}

function splitScheduleTimes(value: string) {
  return value
    .split(/[\n,;]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function splitSourceLines(value: string) {
  return value
    .split(/\r?\n+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function joinSourceLines(values?: string[]) {
  return (values ?? []).join("\n");
}

function externalSourcesToForm(sources?: ExternalProxySources): ExternalProxySourcesFormState {
  return {
    httpTxt: joinSourceLines(sources?.httpTxt),
    httpJSON: joinSourceLines(sources?.httpJSON),
    httpHTML: joinSourceLines(sources?.httpHTML),
    socks5Txt: joinSourceLines(sources?.socks5Txt),
    socks5JSON: joinSourceLines(sources?.socks5JSON),
    socks5HTML: joinSourceLines(sources?.socks5HTML),
  };
}

function buildExternalSourcesPayload(form: ExternalProxySourcesFormState): ExternalProxySources {
  return {
    httpTxt: splitSourceLines(form.httpTxt),
    httpJSON: splitSourceLines(form.httpJSON),
    httpHTML: splitSourceLines(form.httpHTML),
    socks5Txt: splitSourceLines(form.socks5Txt),
    socks5JSON: splitSourceLines(form.socks5JSON),
    socks5HTML: splitSourceLines(form.socks5HTML),
  };
}

function defaultScheduleForm(): ScheduleFormState {
  return {
    enabled: false,
    timesText: "",
    ...defaultAutoImportForm,
  };
}

function scheduleToForm(schedule: ProxyImportSchedule): ScheduleFormState {
  return {
    enabled: schedule.enabled,
    timesText: (schedule.times ?? []).join(", "),
    mode: schedule.mode || "all",
    group: schedule.group || "自动抓取",
    limit: String(schedule.limit || 800),
    concurrency: String(schedule.concurrency || 96),
    timeoutSeconds: String(schedule.timeoutSeconds || 4),
    retryCount: String(schedule.retryCount ?? 0),
    cleanupEnabled: schedule.cleanupEnabled ?? false,
    cleanupMaxLatencyMs: String(schedule.cleanupMaxLatencyMs || 3000),
    cleanupDeleteFailedAutoProxies: schedule.cleanupDeleteFailedAutoProxies ?? false,
  };
}

function taskStatusVariant(status?: string): "default" | "secondary" | "destructive" | "outline" | "ghost" | "link" {
  switch (status) {
    case "running":
      return "secondary";
    case "failed":
      return "destructive";
    case "succeeded":
      return "default";
    default:
      return "outline";
  }
}

function formatTaskStatus(status?: string) {
  switch (status) {
    case "running":
      return "执行中";
    case "succeeded":
      return "已完成";
    case "failed":
      return "失败";
    default:
      return "空闲";
  }
}

function formatTaskPhase(phase?: string) {
  switch (phase) {
    case "fetching":
      return "抓取源";
    case "testing":
      return "测速";
    case "persisting":
      return "写入";
    case "completed":
      return "完成";
    case "failed":
      return "异常";
    default:
      return "待命";
  }
}

function formatTaskTrigger(trigger?: string) {
  switch (trigger) {
    case "scheduled":
      return "定时触发";
    case "manual":
      return "手动触发";
    default:
      return "-";
  }
}

function formatImportMode(value: string) {
  switch (value) {
    case "http":
      return "仅 HTTP";
    case "socks5":
      return "仅 SOCKS5";
    default:
      return "HTTP + SOCKS5";
  }
}

function formatProxyType(value: string) {
  return value === "socks5h" ? "socks5" : value;
}


function formatDate(value: string) {
  return new Date(value).toLocaleString();
}
