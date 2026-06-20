import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Server actions and the engine proxy run server-side; nothing special
  // needed here beyond strict mode.
  reactStrictMode: true,
};

export default nextConfig;
