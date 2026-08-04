import { NextResponse } from "next/server";
import { ADMIN_COOKIE } from "@/lib/admin-auth";

export async function POST(request: Request) {
  const isHttps =
    request.url.startsWith("https://") ||
    request.headers.get("x-forwarded-proto")?.includes("https") ||
    false;
  const response = NextResponse.json({ ok: true });
  response.cookies.set(ADMIN_COOKIE, "", {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    secure: isHttps,
    maxAge: 0,
  });
  return response;
}
