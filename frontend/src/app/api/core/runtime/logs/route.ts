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

export async function GET(request: Request) {
  try {
    const url = new URL(request.url);
    const lines = url.searchParams.get('lines') || '200';
    const res = await adminFetch(`${backend}/admin/core/runtime/logs?lines=${encodeURIComponent(lines)}`, { cache: 'no-store' });
    return forwardJson(res, '读取 Xray 日志失败。');
  } catch {
    return NextResponse.json({ error: '读取 Xray 日志失败。' }, { status: 500 });
  }
}

export async function DELETE() {
  try {
    const res = await adminFetch(`${backend}/admin/core/runtime/logs`, {
      method: 'DELETE',
      cache: 'no-store',
    });
    return forwardJson(res, '清空 Xray 日志失败。');
  } catch {
    return NextResponse.json({ error: '清空 Xray 日志失败。' }, { status: 500 });
  }
}
