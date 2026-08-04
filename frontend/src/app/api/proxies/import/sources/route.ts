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
    const res = await adminFetch(`${backend}/admin/proxies/import/sources`, { cache: 'no-store' });
    return forwardJson(res, '\u8bfb\u53d6\u5916\u7f6e\u4ee3\u7406\u6e90\u914d\u7f6e\u5931\u8d25\u3002');
  } catch {
    return NextResponse.json({ error: '\u8bfb\u53d6\u5916\u7f6e\u4ee3\u7406\u6e90\u914d\u7f6e\u5931\u8d25\u3002' }, { status: 500 });
  }
}

export async function PUT(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const res = await adminFetch(`${backend}/admin/proxies/import/sources`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      cache: 'no-store',
      body: JSON.stringify(body),
    });
    return forwardJson(res, '\u4fdd\u5b58\u5916\u7f6e\u4ee3\u7406\u6e90\u914d\u7f6e\u5931\u8d25\u3002');
  } catch {
    return NextResponse.json({ error: '\u4fdd\u5b58\u5916\u7f6e\u4ee3\u7406\u6e90\u914d\u7f6e\u5931\u8d25\u3002' }, { status: 500 });
  }
}
