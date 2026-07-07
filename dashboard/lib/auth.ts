import NextAuth from "next-auth";

declare module "next-auth" {
  interface Session {
    accessToken?: string;
    error?: string;
    user: {
      id: string;
      name?: string | null;
      email?: string | null;
      image?: string | null;
      roles?: string[];
    };
  }
}

// Refresh an expired access token against the rxlab-auth token endpoint. The
// refresh token rotates on every call, so we persist whichever one comes back.
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
      // Initial login — keep the real OIDC sub (not NextAuth's internal id).
      // The rxlab-auth id token carries the per-client `roles` claim used to
      // gate the admin console.
      if (account) {
        const { exp: _exp, ...rest } = token;
        console.log("[admin auth] initial login token received", {
          hasRefreshToken: Boolean(account.refresh_token),
          expiresAt: account.expires_at ?? null,
        });
        return {
          ...rest,
          accessToken: account.access_token,
          refreshToken: account.refresh_token,
          // Keep the OAuth access-token expiry separate from the reserved JWT
          // `exp` claim. `exp` controls the AuthJS session cookie lifetime; if
          // it is pinned to the access-token expiry, the whole admin session is
          // discarded before the refresh-token branch can run.
          expiresAt: account.expires_at,
          userId: profile?.sub,
          roles: Array.isArray(profile?.roles) ? (profile.roles as string[]) : [],
        };
      }
      // Still valid (refresh 1 minute early).
      if (
        token.expiresAt &&
        Date.now() < (token.expiresAt as number) * 1000 - 60_000
      ) {
        return token;
      }
      const { exp: _exp, ...rest } = token;
      console.log("[admin auth] access token refresh needed", {
        hasRefreshToken: Boolean(token.refreshToken),
        expiresAt: token.expiresAt ?? null,
      });
      if (!token.refreshToken) {
        console.warn("[admin auth] cannot refresh access token: no refresh token");
        return { ...rest, error: "RefreshTokenError" };
      }
      try {
        const fresh = await refreshAccessToken(token.refreshToken as string);
        console.log("[admin auth] access token refreshed", {
          hasRefreshToken: Boolean(fresh.refreshToken),
          expiresAt: fresh.expiresAt,
        });
        return {
          ...rest,
          accessToken: fresh.accessToken,
          refreshToken: fresh.refreshToken,
          expiresAt: fresh.expiresAt,
          error: undefined,
        };
      } catch (err) {
        console.error("[admin auth] access token refresh failed", {
          hasRefreshToken: Boolean(token.refreshToken),
          error: err instanceof Error ? err.message : String(err),
        });
        return { ...rest, error: "RefreshTokenError" };
      }
    },
    async session({ session, token }) {
      if (token.userId) session.user.id = token.userId as string;
      if (token.name) session.user.name = token.name as string;
      if (token.email) session.user.email = token.email as string;
      session.user.roles = (token.roles as string[] | undefined) ?? [];
      session.accessToken = token.accessToken as string | undefined;
      session.error = token.error as string | undefined;
      return session;
    },
  },
  pages: { signIn: "/login" },
  trustHost: true,
  // The session should outlive short OAuth access tokens; access-token refresh
  // is handled in the jwt() callback with the stored refresh token.
  session: { strategy: "jwt", maxAge: 30 * 24 * 60 * 60 },
});

export const { handlers, signIn, signOut, auth } = nextAuth;
