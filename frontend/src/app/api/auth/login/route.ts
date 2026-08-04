import { NextResponse } from "next/server";
import { verifyAdminToken, ADMIN_COOKIE } from "@/lib/admin-auth";

export async function POST(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const password = String(body?.password || "").trim();
    if (!password) {
      return NextResponse.json({ error: "请输入管理后台密码。" }, { status: 400 });
    }

    const ok = await verifyAdminToken(password);
    if (!ok) {
      return NextResponse.json({ error: "密码错误。" }, { status: 401 });
    }

    // HTTP 访问时不能设 Secure（浏览器会拒绝保存），按实际请求协议判断
    const isHttps =
      request.url.startsWith("https://") ||
      request.headers.get("x-forwarded-proto")?.includes("https") ||
      false;
    const response = NextResponse.json({ ok: true });
    response.cookies.set(ADMIN_COOKIE, password, {
      httpOnly: true,
      sameSite: "lax",
      secure: isHttps,
      path: "/",
      maxAge: 60 * 60 * 24 * 7,
    });
    return response;
  } catch {
    return NextResponse.json({ error: "登录失败。" }, { status: 500 });
  }
}
