import type { Metadata } from "next";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import "../globals.css";

import AdminShell from "@/components/dashboard/admin-shell";

export const metadata: Metadata = {
  title: "NVIDIA API 网关 | 管理后台",
  description: "NVIDIA API 网关管理后台",
};

export default async function AdminLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const store = await cookies();
  const token = store.get("admin_token")?.value || "";
  // 未登录（无 admin_token cookie）时跳转登录页。
  // 若 cookie 存在但已失效，后续 /api/* 请求会返回 401，由各页面提示重新登录。
  if (!token) {
    redirect("/login");
  }

  return <AdminShell>{children}</AdminShell>;
}
