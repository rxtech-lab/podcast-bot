import { auth } from "@/lib/auth";

// Next.js 16 "proxy" convention (formerly middleware.ts).
// Auth gate for the admin console:
//   - Unauthenticated users are sent to /login.
//   - Authenticated users without the "admin" role are sent to /forbidden.
// The Go admin API independently enforces the same role on every request, so
// this is a UX gate, not the security boundary.
export default auth((req) => {
  const { pathname, origin } = req.nextUrl;

  // The Playwright stack runs against the backend's disposable E2E database
  // and fixed admin identity. Keep this bypass behind the same explicit mode;
  // production still requires a real AuthJS session and admin role.
  if (process.env.E2E_MODE === "true") return;

  if (!req.auth) {
    if (pathname === "/login") return;
    return Response.redirect(new URL("/login", origin));
  }

  const isAdmin = req.auth.user?.roles?.includes("admin") ?? false;
  if (!isAdmin && pathname !== "/forbidden") {
    return Response.redirect(new URL("/forbidden", origin));
  }
});

export const config = {
  matcher: ["/((?!api/auth|login|forbidden|_next/static|_next/image|favicon.ico).*)"],
};
