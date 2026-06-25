import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  distDir: process.env.E2E_AUTH === "1" ? ".next-e2e" : ".next",
};

export default nextConfig;
