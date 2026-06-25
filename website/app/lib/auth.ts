import NextAuth from "next-auth";

// rxlab-auth (auth.js / NextAuth) for the public podcast viewer. Mirrors the
// dashboard's setup: the "rxlab" OIDC provider with refresh-token rotation, and
// the rxlab access token exposed on the session so server components can call
// the Go backend on the user's behalf (own + public discussions).

declare module "next-auth" {
  interface Session {
    accessToken?: string;
    error?: string;
    user: {
      id: string;
      name?: string | null;
      email?: string | null;
      image?: string | null;
    };
  }
}

async function refreshAccessToken(refreshToken: string) {
  const response = await fetch(`${process.env.AUTH_ISSUER}/api/oauth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "refresh_token",
      refresh_token: refreshToken,
      client_id: process.env.AUTH_CLIENT_ID!,
      client_secret: process.env.AUTH_CLIENT_SECRET!,
    }),
  });
  const tokens = await response.json();
  if (!response.ok) throw tokens;
  return {
    accessToken: tokens.access_token as string,
    refreshToken: (tokens.refresh_token ?? refreshToken) as string,
    expiresAt: Math.floor(Date.now() / 1000) + tokens.expires_in,
  };
}

const nextAuth = NextAuth({
  debug: process.env.NODE_ENV === "development",
  providers: [
    {
      id: "rxlab",
      name: "RxLab",
      type: "oidc",
      issuer: process.env.AUTH_ISSUER,
      clientId: process.env.AUTH_CLIENT_ID!,
      clientSecret: process.env.AUTH_CLIENT_SECRET!,
      client: {
        token_endpoint_auth_method: "client_secret_post",
      },
      authorization: {
        params: { scope: "openid email profile offline_access" },
      },
    },
  ],
  callbacks: {
    async jwt({ token, account, profile }) {
      if (account) {
        return {
          ...token,
          accessToken: account.access_token,
          refreshToken: account.refresh_token,
          exp: account.expires_at,
          userId: profile?.sub,
        };
      }
      if (token.exp && Date.now() < (token.exp as number) * 1000 - 60_000) {
        return token;
      }
      if (!token.refreshToken) {
        return { ...token, error: "RefreshTokenError" };
      }
      try {
        const fresh = await refreshAccessToken(token.refreshToken as string);
        return {
          ...token,
          accessToken: fresh.accessToken,
          refreshToken: fresh.refreshToken,
          exp: fresh.expiresAt,
          error: undefined,
        };
      } catch {
        return { ...token, error: "RefreshTokenError" };
      }
    },
    async session({ session, token }) {
      if (token.userId) session.user.id = token.userId as string;
      if (token.name) session.user.name = token.name as string;
      if (token.email) session.user.email = token.email as string;
      session.accessToken = token.accessToken as string | undefined;
      session.error = token.error as string | undefined;
      return session;
    },
  },
  pages: { signIn: "/login" },
  trustHost: true,
  // Use an app-specific cookie name so the viewer's session never collides with
  // the dashboard's when both run on the same localhost origin (they have
  // different AUTH_SECRETs; a shared cookie name causes "no matching decryption
  // secret" errors when one app reads the other's cookie).
  cookies: {
    sessionToken: {
      name: "podcast-viewer.session-token",
      options: {
        httpOnly: true,
        sameSite: "lax",
        path: "/",
        secure: process.env.NODE_ENV === "production",
      },
    },
  },
});

export const { handlers, signIn, signOut, auth } = nextAuth;
