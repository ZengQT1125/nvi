import { NextResponse } from 'next/server';

import { adminFetch } from '@/lib/admin-auth';
const backend = process.env.API_BASE_URL || 'http://localhost:18080';

async function forwardJson(res: Response, fallbackMessage: string) {
  const text = await res.text();
  if (!text) {
    return NextResponse.json({ success: res.ok }, { status: res.status });
  }

  try {
    return NextResponse.json(JSON.parse(text), { status: res.status });
  } catch {
    return NextResponse.json({ error: fallbackMessage, detail: text }, { status: res.status });
  }
}

export async function GET() {
  try {
    const res = await adminFetch(`${backend}/admin/health/report`, { cache: 'no-store' });
    return forwardJson(res, '读取系统健康报告失败。');
  } catch {
    return NextResponse.json({ error: '读取系统健康报告失败。' }, { status: 500 });
  }
}

export async function POST(request: Request) {
  try {
    const body = await request.text();
    // 把浏览器侧的 AbortSignal 透传给后端 fetch：
    // 如果用户在前端取消（超时 / 离开页面），后端的 /admin/health/report/run
    // 会立刻收到 ctx Done，停止继续做模型探测，不会再变成"幽灵任务"占用上游连接。
    const res = await adminFetch(`${backend}/admin/health/report/run`, {
      method: 'POST',
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      cache: 'no-store',
      body: body || undefined,
      signal: request.signal,
    });
    return forwardJson(res, '执行系统健康检查失败。');
  } catch (err) {
    const aborted = err instanceof DOMException && err.name === 'AbortError';
    if (aborted) {
      return NextResponse.json({ error: '健康检查请求已取消。' }, { status: 499 });
    }
    return NextResponse.json({ error: '执行系统健康检查失败。' }, { status: 500 });
  }
}
