import { cookies } from "next/headers";

const backend = process.env.API_BASE_URL || "http://localhost:18080";

export const ADMIN_COOKIE = "admin_token";

// adminFetch 与后端通信，并自动附带登录 cookie 中的 X-Admin-Token。
// 所有 /api/* 的 server route handler 都通过它转发到后端 /admin/*，
// 保证后端 AdminAuthMiddleware 能校验到当前登录态。
export async function adminFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const store = await cookies();
  const token = store.get(ADMIN_COOKIE)?.value || "";
  const headers = new Headers(init.headers || {});
  if (token) {
    headers.set("X-Admin-Token", token);
  }
  return fetch(url, { ...init, headers });
}

// 供登录页提交密码：将密码作为 X-Admin-Token 发给后端 /admin/auth/check。
export async function verifyAdminToken(token: string): Promise<boolean> {
  try {
    const res = await fetch(`${backend}/admin/auth/check`, {
      method: "GET",
      headers: { "X-Admin-Token": token },
      cache: "no-store",
    });
    return res.ok;
  } catch {
    return false;
  }
}
