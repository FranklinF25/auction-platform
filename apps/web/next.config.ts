import type { NextConfig } from "next";

// The browser only ever talks to the Next.js origin: /api/* requests are
// proxied to the Go API by middleware.ts, which resolves API_ORIGIN per
// request at runtime — so the backend needs zero CORS code and the build
// output stays environment-independent. (Proxies must NOT be declared here:
// rewrites are serialized into .next at build time, which froze the default
// origin into the standalone server and broke the runtime API_ORIGIN env.)

const nextConfig: NextConfig = {
  output: "standalone",
  // CI lints with `npm run lint` (eslint 9 flat config); skip the deprecated
  // build-time lint pass so builds stay fast and deterministic.
  eslint: {
    ignoreDuringBuilds: true,
  },
};

export default nextConfig;
