import type { NextConfig } from 'next';

// A fully static export (out/), served by nginx (nginx/, Dockerfile). No server runtime.
const nextConfig: NextConfig = {
  output: 'export',
  trailingSlash: false,
  reactStrictMode: true,
  // Screenshots are pre-generated in every size and format (scripts/screenshots.mjs).
  images: { unoptimized: true },
  experimental: {
    // One 404 page for both root layouts (English at /, German under /de).
    globalNotFound: true,
  },
};

export default nextConfig;
