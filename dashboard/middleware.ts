import { auth } from "@/lib/auth";

// Protect every route except the auth handler, the login page, the engine
// proxy (which does its own session check), and static assets.
export default auth((req) => {
  if (!req.auth && req.nextUrl.pathname !== "/login") {
    const url = new URL("/login", req.nextUrl.origin);
    return Response.redirect(url);
  }
});

export const config = {
  matcher: [
    "/((?!api/auth|login|_next/static|_next/image|favicon.ico).*)",
  ],
};
