import { NextResponse } from 'next/server';

import { adminFetch } from '@/lib/admin-auth';
const backend = process.env.API_BASE_URL || 'http://localhost:18080';

export async function GET() {
  try {
    const res = await adminFetch(`${backend}/admin/keys/export`, {
      cache: 'no-store',
    });
    if (!res.ok) {
      return NextResponse.json({ error: '导出失败。' }, { status: res.status });
    }
    const text = await res.text();
    return new NextResponse(text, {
      status: 200,
      headers: {
        'Content-Type': 'text/plain; charset=utf-8',
        'Content-Disposition': 'attachment; filename=api_keys_export.txt',
      },
    });
  } catch {
    return NextResponse.json({ error: '导出失败。' }, { status: 500 });
  }
}
