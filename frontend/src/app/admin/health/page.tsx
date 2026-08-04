"use client";

import { useState } from "react";
import useSWR from "swr";

import { MetricCard, StatusBadge } from "@/components/dashboard/visuals";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Select } from "@/components/ui/select";

const fetcher = async (url: string) => {
  const res = await fetch(url, { cache: "no-store" });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(data?.error || "request_failed");
  }
  return data;
};

type HealthReport = {
  generatedAt: string;
  upstreamBaseURL: string;
  probeKeyName: string;
  probeKeyDedicated: boolean;
  probeTimeoutSecond: number;
  summary: {
    overallStatus: string;
    totalKeys: number;
    activeKeys: number;
    coolingKeys: number;
    deadKeys: number;
    disabledKeys: number;
    healthyChecks: number;
    unhealthyChecks: number;
    avgLatencyMs: number;
  };
  checks: Array<{
    id: string;
    title: string;
    method: string;
    endpoint: string;
    success: boolean;
    httpStatus: number;
    durationMs: number;
    statusLabel: string;
    detail: string;
    meta?: Record<string, unknown>;
  }>;
  recommendations: string[];
  modelCatalog: Array<{
    id: string;
    supportsChatCandidate: boolean;
    supportsEmbeddingsCandidate: boolean;
  }>;
  activeRun?: ModelRun | null;
  fullSweep?: ModelRun | null;
};

type ModelRun = {
  generatedAt: string;
  scope: string;
  protocol: string;
  selectedModelId: string;
  summary: {
    total: number;
    healthy: number;
    failed: number;
    avgLatencyMs: number;
  };
  checks: Array<{
    generatedAt: string;
    modelId: string;
    protocol: string;
    method: string;
    endpoint: string;
    success: boolean;
    httpStatus: number;
    durationMs: number;
    statusLabel: string;
    detail: string;
    attemptCount: number;
    meta?: Record<string, unknown>;
  }>;
};

type UpstreamRuntimeSnapshot = {
  generatedAt: string;
  summary: {
    totalKeys: number;
    activeKeys: number;
    coolingKeys: number;
    deadKeys: number;
    disabledKeys: number;
    schedulerStats?: {
      active: number;
      cooling: number;
      dead: number;
    } | null;
  };
  lastEvent?: {
    at: string;
    operation: string;
    operationLabel?: string;
    stage: string;
    stageLabel?: string;
    sourceType?: string;
    sourceLabel?: string;
    upstreamKeyName?: string;
    success: boolean;
    httpStatus?: number;
    detail?: string;
    rawDetail?: string;
  } | null;
  recentEvents: Array<{
    at: string;
    operation: string;
    operationLabel?: string;
    stage: string;
    stageLabel?: string;
    sourceType?: string;
    sourceLabel?: string;
    upstreamKeyName?: string;
    success: boolean;
    httpStatus?: number;
    detail?: string;
    rawDetail?: string;
  }>;
};

export default function HealthPage() {
  const { data, error, mutate, isLoading } = useSWR<HealthReport>("/api/health/report", fetcher);
  const { data: runtimeData } = useSWR<UpstreamRuntimeSnapshot>("/api/upstream/runtime", fetcher, { refreshInterval: 5000 });

  const [running, setRunning] = useState(false);
  const [scope, setScope] = useState<"all" | "single">("all");
  const [protocol, setProtocol] = useState<"auto" | "chat" | "embeddings">("auto");
  const [selectedModelId, setSelectedModelId] = useState("");
  const [runError, setRunError] = useState<string | null>(null);

  const modelCatalog = Array.isArray(data?.modelCatalog) ? data.modelCatalog : [];
  const effectiveSelectedModelId = selectedModelId || modelCatalog[0]?.id || "";

  const runHealthCheck = async () => {
    setRunning(true);
    setRunError(null);
    // 全量扫描默认会按目录里的每个模型挨个做真实 chat / embeddings 探测，
    // 在上游不稳定的时候单次扫描可能跑十几分钟。给前端 fetch 加一个上限，
    // 避免按钮永远停在 "检查中..."，让用户能拿到一个明确的失败提示而不是无尽等待。
    const controller = new AbortController();
    const timeoutMs = scope === "all" ? 10 * 60 * 1000 : 3 * 60 * 1000;
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const res = await fetch("/api/health/report", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          scope,
          modelId: scope === "single" ? effectiveSelectedModelId : "",
          protocol,
        }),
        signal: controller.signal,
      });
      const payload = await res.json().catch(() => null);
      if (!res.ok) {
        setRunError(payload?.error || "执行健康检查失败。");
        return;
      }
      await mutate(payload as HealthReport, { revalidate: false });
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err);
      const aborted = err instanceof DOMException && err.name === "AbortError";
      setRunError(
        aborted
          ? `健康检查超时（${Math.round(timeoutMs / 1000)}s）。请确认上游网络/代理是否正常，或改为单模型模式定位问题。`
          : `执行健康检查失败：${reason}`,
      );
    } finally {
      clearTimeout(timer);
      setRunning(false);
    }
  };

  return (
    <div className="space-y-6 text-slate-900">
      <section className="rounded-[30px] border border-slate-200/70 bg-[radial-gradient(circle_at_top_left,rgba(186,230,253,0.7),transparent_28%),linear-gradient(180deg,rgba(255,255,255,0.96),rgba(248,250,252,0.94))] p-8 shadow-sm">
        <div className="flex flex-col gap-6 xl:flex-row xl:items-end xl:justify-between">
          <div>
            <div className="text-xs uppercase tracking-[0.24em] text-slate-400">健康检查</div>
            <h1 className="mt-3 text-3xl font-semibold tracking-tight text-slate-900 md:text-4xl">查看网关和所有模型是否正常</h1>
            <p className="mt-3 max-w-3xl text-sm leading-7 text-slate-500">
              当前页面默认只展示缓存结果，不会在打开时自动探测上游；只有点击“立即检查”后，才会真实访问 NVIDIA 官方 API。上游池实时状态区域只读取网关内存快照，不会额外请求官方。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            {data?.summary?.overallStatus ? <StatusBadge status={data.summary.overallStatus} className="text-xs" /> : null}
            <div className="rounded-full border border-slate-200 bg-white px-4 py-2 text-sm text-slate-600">
              最近报告：{data?.generatedAt ? new Date(data.generatedAt).toLocaleString() : "还没有"}
            </div>
          </div>
        </div>
      </section>

      <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
        <CardHeader>
          <CardTitle>执行健康检查</CardTitle>
          <CardDescription>点击“立即检查”才会真实访问 NVIDIA 官方 API。单模型模式下，会用你选定的模型做真实请求。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-3">
            <label className="space-y-2 block">
              <span className="text-sm text-slate-500">检查范围</span>
              <Select value={scope} onChange={(e) => setScope(e.target.value as "all" | "single") }>
                <option value="all">全部模型</option>
                <option value="single">单个模型</option>
              </Select>
            </label>
            <label className="space-y-2 block">
              <span className="text-sm text-slate-500">协议</span>
              <Select value={protocol} onChange={(e) => setProtocol(e.target.value as "auto" | "chat" | "embeddings") }>
                <option value="auto">自动</option>
                <option value="chat">Chat</option>
                <option value="embeddings">Embeddings</option>
              </Select>
            </label>
            <label className="space-y-2 block">
              <span className="text-sm text-slate-500">模型</span>
              <Select value={effectiveSelectedModelId} onChange={(e) => setSelectedModelId(e.target.value)} disabled={scope !== "single" || modelCatalog.length === 0}>
                {(modelCatalog.length > 0 ? modelCatalog : [{ id: "", supportsChatCandidate: false, supportsEmbeddingsCandidate: false }]).map((item, index) => (
                  <option key={`${item.id}-${index}`} value={item.id}>{item.id || "暂无模型目录"}</option>
                ))}
              </Select>
            </label>
          </div>
          {scope === "single" && modelCatalog.length === 0 ? (
            <div className="rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
              当前还没有缓存模型目录。请先执行一次“立即检查”，拿到最新模型列表后，再做单模型探测。
            </div>
          ) : null}
          <div className="flex gap-3">
            <Button onClick={runHealthCheck} disabled={running || (scope === "single" && !effectiveSelectedModelId)}>
              {running ? "检查中..." : "立即检查"}
            </Button>
          </div>
        </CardContent>
      </Card>

      {error ? <div className="rounded-2xl border border-rose-200 bg-rose-50 px-5 py-4 text-sm text-rose-700">读取健康报告失败：{error.message}</div> : null}
      {runError ? <div className="rounded-2xl border border-rose-200 bg-rose-50 px-5 py-4 text-sm text-rose-700">{runError}</div> : null}
      {isLoading && !data ? <div className="rounded-2xl border border-slate-200 bg-white px-5 py-12 text-center text-sm text-slate-500">正在读取健康报告...</div> : null}

      {runtimeData ? (
        <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
          <CardHeader>
            <CardTitle>上游池实时状态</CardTitle>
            <CardDescription>这里只读取网关内存里的实时快照，不会额外请求 NVIDIA 官方 API；只有“来源”显示为“官方 HTTP 返回”或“上游网络错误”时，才代表真实上游证据。</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-5">
              <MetricCard label="总上游 Key" value={`${runtimeData.summary.totalKeys}`} tone="neutral" />
              <MetricCard label="可用 Key" value={`${runtimeData.summary.activeKeys}`} tone="success" />
              <MetricCard label="冷却中" value={`${runtimeData.summary.coolingKeys}`} tone={runtimeData.summary.coolingKeys > 0 ? "warning" : "neutral"} />
              <MetricCard label="已隔离" value={`${runtimeData.summary.deadKeys}`} tone={runtimeData.summary.deadKeys > 0 ? "warning" : "neutral"} />
              <MetricCard label="调度池" value={`${runtimeData.summary.schedulerStats?.active ?? 0}`} delta={`冷却 ${runtimeData.summary.schedulerStats?.cooling ?? 0} / 隔离 ${runtimeData.summary.schedulerStats?.dead ?? 0}`} tone="accent" />
            </div>

            <div className="rounded-2xl border border-slate-200 bg-slate-50 p-4 text-sm text-slate-700">
              <div className="mb-2 text-xs uppercase tracking-[0.2em] text-slate-400">最近一次上游事件</div>
              {runtimeData.lastEvent ? (
                <div className="space-y-3">
                  <div>时间：{new Date(runtimeData.lastEvent.at).toLocaleString()}</div>
                  <div>
                    操作：<span className="font-medium text-slate-900">{runtimeData.lastEvent.operationLabel || runtimeData.lastEvent.operation}</span>
                    <span className="ml-2 font-mono text-xs text-slate-500">{runtimeData.lastEvent.operation}</span>
                  </div>
                  <div>
                    阶段：<span className="font-medium text-slate-900">{runtimeData.lastEvent.stageLabel || runtimeData.lastEvent.stage}</span>
                    <span className="ml-2 font-mono text-xs text-slate-500">{runtimeData.lastEvent.stage}</span>
                  </div>
                  <div>来源：<span className="font-medium text-slate-900">{runtimeData.lastEvent.sourceLabel || "网关内部阶段"}</span></div>
                  <div>上游 Key：<span className="font-medium text-slate-900">{runtimeData.lastEvent.upstreamKeyName || "（还未选中任何 NVIDIA 官方 Key）"}</span></div>
                  <div>结果：{runtimeData.lastEvent.success ? "成功" : "失败"}{runtimeData.lastEvent.httpStatus ? ` / HTTP ${runtimeData.lastEvent.httpStatus}` : ""}</div>
                  {runtimeData.lastEvent.detail ? <div className="break-all text-slate-600">摘要：{runtimeData.lastEvent.detail}</div> : null}
                  {runtimeData.lastEvent.rawDetail ? (
                    <div className="rounded-xl border border-slate-200 bg-white p-3">
                      <div className="text-xs uppercase tracking-[0.2em] text-slate-400">真实上游细节 / 原始证据</div>
                      <pre className="mt-2 whitespace-pre-wrap break-all font-mono text-xs text-slate-700">{runtimeData.lastEvent.rawDetail}</pre>
                    </div>
                  ) : (
                    <div className="rounded-xl border border-dashed border-slate-200 bg-white p-3 text-xs text-slate-500">这条记录只有网关内部阶段，没有可展示的官方原始返回。</div>
                  )}
                </div>
              ) : (
                <div className="text-slate-500">暂时还没有上游运行事件。</div>
              )}
            </div>

            {runtimeData.recentEvents.length > 0 ? (
              <div className="max-h-[28rem] overflow-y-auto rounded-2xl border border-slate-200 bg-white">
                <table className="min-w-full text-sm">
                  <thead>
                    <tr className="border-b border-slate-200 text-left text-slate-400">
                      <th className="px-3 py-3">时间</th>
                      <th className="px-3 py-3">操作</th>
                      <th className="px-3 py-3">阶段</th>
                      <th className="px-3 py-3">来源</th>
                      <th className="px-3 py-3">上游 Key</th>
                      <th className="px-3 py-3">结果</th>
                      <th className="px-3 py-3">真实细节</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...runtimeData.recentEvents].reverse().map((event, index) => (
                      <tr key={`${event.at}-${event.operation}-${index}`} className="border-b border-slate-100 align-top text-slate-700">
                        <td className="px-3 py-3 whitespace-nowrap">{new Date(event.at).toLocaleTimeString()}</td>
                        <td className="px-3 py-3">
                          <div className="font-medium text-slate-900">{event.operationLabel || event.operation}</div>
                          <div className="font-mono text-[11px] text-slate-500">{event.operation}</div>
                        </td>
                        <td className="px-3 py-3">
                          <div className="font-medium text-slate-900">{event.stageLabel || event.stage}</div>
                          <div className="font-mono text-[11px] text-slate-500">{event.stage}</div>
                        </td>
                        <td className="px-3 py-3">{event.sourceLabel || "网关内部阶段"}</td>
                        <td className="px-3 py-3">{event.upstreamKeyName || "-"}</td>
                        <td className="px-3 py-3">{event.success ? "成功" : "失败"}{event.httpStatus ? ` · ${event.httpStatus}` : ""}</td>
                        <td className="px-3 py-3 max-w-xl whitespace-pre-wrap break-all">{event.rawDetail || event.detail || "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
          </CardContent>
        </Card>
      ) : null}

      {data ? (
        <>
          <section className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <MetricCard label="上游 Key" value={`${data.summary.totalKeys}`} delta={`可用 ${data.summary.activeKeys}`} tone="accent" />
            <MetricCard label="基线检查" value={`${data.summary.healthyChecks}/${data.summary.healthyChecks + data.summary.unhealthyChecks}`} delta={`平均 ${Math.round(data.summary.avgLatencyMs)} ms`} tone={data.summary.unhealthyChecks ? "warning" : "success"} />
            <MetricCard label="全模型数量" value={`${data.fullSweep?.summary.total ?? 0}`} delta={`健康 ${data.fullSweep?.summary.healthy ?? 0}`} tone="neutral" />
            <MetricCard label="全模型平均延迟" value={`${Math.round(data.fullSweep?.summary.avgLatencyMs ?? 0)} ms`} delta={data.fullSweep ? `失败 ${data.fullSweep.summary.failed}` : "请先跑全部模型检查"} tone={data.fullSweep?.summary.failed ? "warning" : "success"} />
          </section>

          <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
            <CardHeader>
              <CardTitle>网关基线健康</CardTitle>
              <CardDescription>这里展示最近一次基线探测结果，包括状态码、耗时和官方/网关返回的说明。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              {(data.checks ?? []).map((check) => (
                <div key={check.id} className="rounded-2xl border border-slate-200 bg-slate-50 p-4">
                  <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                    <div>
                      <div className="text-lg font-semibold text-slate-900">{check.title}</div>
                      <div className="mt-1 text-xs font-mono text-slate-500">{check.method} {check.endpoint}</div>
                    </div>
                    <div className="text-sm text-slate-600">
                      {check.success ? "成功" : "失败"} · HTTP {check.httpStatus || 0} · {check.durationMs} ms
                    </div>
                  </div>
                  <div className="mt-3 text-sm text-slate-700 whitespace-pre-wrap break-all">{check.detail}</div>
                </div>
              ))}
            </CardContent>
          </Card>

          {data.recommendations?.length ? (
            <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
              <CardHeader>
                <CardTitle>建议与说明</CardTitle>
                <CardDescription>这里保留系统当前给出的说明，便于你核对运行状态。</CardDescription>
              </CardHeader>
              <CardContent className="space-y-3 text-sm text-slate-700">
                {data.recommendations.map((item, index) => (
                  <div key={`${item}-${index}`} className="rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3">{item}</div>
                ))}
              </CardContent>
            </Card>
          ) : null}

          {data.activeRun ? <ModelRunCard title="最近一次实时模型检查" description="这里展示你最后一次点击“立即检查”得到的真实结果。" run={data.activeRun} /> : null}
          {data.fullSweep ? <ModelRunCard title="最近一次全模型结果" description="这里展示最近一次全模型检查的真实结果汇总。" run={data.fullSweep} /> : null}
        </>
      ) : null}
    </div>
  );
}

function ModelRunCard({ title, description, run }: { title: string; description: string; run: ModelRun }) {
  return (
    <Card className="border border-slate-200/70 bg-white/90 shadow-sm">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-3 md:grid-cols-4">
          <MetricCard label="总数" value={`${run.summary.total}`} tone="neutral" />
          <MetricCard label="成功" value={`${run.summary.healthy}`} tone="success" />
          <MetricCard label="失败" value={`${run.summary.failed}`} tone={run.summary.failed > 0 ? "warning" : "neutral"} />
          <MetricCard label="平均延迟" value={`${Math.round(run.summary.avgLatencyMs)} ms`} tone="accent" />
        </div>
        <div className="rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-700">
          生成时间：{new Date(run.generatedAt).toLocaleString()} · 范围：{run.scope} · 协议：{run.protocol} · 选中模型：{run.selectedModelId || "-"}
        </div>
        <div className="max-h-[28rem] overflow-y-auto rounded-2xl border border-slate-200 bg-white">
          <table className="min-w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-left text-slate-400">
                <th className="px-3 py-3">模型</th>
                <th className="px-3 py-3">协议</th>
                <th className="px-3 py-3">结果</th>
                <th className="px-3 py-3">耗时</th>
                <th className="px-3 py-3">说明</th>
              </tr>
            </thead>
            <tbody>
              {run.checks.map((check, index) => (
                <tr key={`${check.modelId}-${check.protocol}-${index}`} className="border-b border-slate-100 align-top text-slate-700">
                  <td className="px-3 py-3 break-all">{check.modelId}</td>
                  <td className="px-3 py-3">{check.protocol}</td>
                  <td className="px-3 py-3">{check.success ? "成功" : "失败"}{check.httpStatus ? ` · ${check.httpStatus}` : ""}</td>
                  <td className="px-3 py-3 whitespace-nowrap">{check.durationMs} ms</td>
                  <td className="px-3 py-3 max-w-xl whitespace-pre-wrap break-all">{check.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  );
}
