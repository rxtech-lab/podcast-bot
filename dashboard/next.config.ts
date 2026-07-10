import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Playwright uses its own build directory so it can run while a developer's
  // normal `next dev` process is already using `.next`.
  distDir: process.env.NEXT_DIST_DIR ?? ".next",
  // Server actions and the engine proxy run server-side; nothing special
  // needed here beyond strict mode.
  reactStrictMode: true,
};

export default nextConfig;
