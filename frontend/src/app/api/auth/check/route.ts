import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import { verifyAdminToken, ADMIN_COOKIE } from "@/lib/admin-auth";

export async function GET() {
  const store = await cookies();
  const token = store.get(ADMIN_COOKIE)?.value || "";
  if (!token) {
    return NextResponse.json({ ok: false }, { status: 401 });
  }
  const ok = await verifyAdminToken(token);
  if (!ok) {
    return NextResponse.json({ ok: false }, { status: 401 });
  }
  return NextResponse.json({ ok: true });
}
