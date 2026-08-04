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

export async function DELETE(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const res = await adminFetch(`${backend}/admin/proxies/batch`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      cache: 'no-store',
      body: JSON.stringify(body),
    });
    return forwardJson(res, '批量删除代理失败。');
  } catch {
    return NextResponse.json({ error: '批量删除代理失败。' }, { status: 500 });
  }
}
