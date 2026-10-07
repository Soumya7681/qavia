import type { NextConfig } from "next";

import { apiBaseUrl } from "./src/lib/config";

const nextConfig: NextConfig = {
  cacheComponents: true,
  partialPrefetching: true,

  // The browser calls the API at a relative /api/v1 path and Next forwards it.
  // Same origin means the API's session cookie needs no CORS and no SameSite=None,
  // and the browser never learns a second host to talk to. Server components call
  // apiBaseUrl directly.
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${apiBaseUrl}/api/:path*` }];
  },

  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
