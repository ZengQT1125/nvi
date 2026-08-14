import { NextResponse } from 'next/server';

import { adminFetch } from '@/lib/admin-auth';
const backend = process.env.API_BASE_URL || 'http://localhost:18080';

export async function GET() {
  try {
    const res = await adminFetch(`${backend}/admin/version`, {
      cache: 'no-store',
    });
    if (!res.ok) {
      return NextResponse.json({ version: null }, { status: res.status });
    }
    const data = await res.json().catch(() => null);
    return NextResponse.json(data || { version: null });
  } catch {
    return NextResponse.json({ version: null }, { status: 500 });
  }
}
