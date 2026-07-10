import "server-only";
import { defineAdminConfig } from "@rxtech-lab/admin-generator-next/server";
import { auth } from "@/lib/auth";

// Server-only admin configuration consumed by <AdminApp> and the admin server
// actions. getToken forwards the signed-in user's own rxlab access token as the
// bearer, so the Go admin API validates it and enforces the "admin" role per
// request (defense in depth with the middleware gate). We deliberately do NOT
// use the shared service token here — admin actions must run as the admin user.
export const adminConfig = defineAdminConfig({
  apiUrl: process.env.ENGINE_BASE_URL ?? "http://localhost:8080",
  basePath: "/admin",
  getToken: async () =>
    process.env.E2E_MODE === "true"
      ? null
      : (await auth())?.accessToken ?? null,
});
