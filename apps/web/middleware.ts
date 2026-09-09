import { NextResponse, type NextRequest } from "next/server";

// Runtime /api proxy: the browser only ever talks to the Next.js origin, so the
// Go API needs zero CORS code. This runs per request in both `next dev` and the
// standalone production server, so API_ORIGIN is read from the live
// environment at request time — image stays environment-agnostic. (The old
// next.config.ts rewrites were serialized into .next at build time with the
// default origin, so the standalone server ignored the runtime API_ORIGIN env.)
//
// The Go API mounts its routes under /api at the origin root (chi
// r.Route("/api", ...) in apps/api/internal/httpapi/server.go), so browser
// /api/auctions maps 1:1 to ${origin}/api/auctions — no path prefix needed.
export function middleware(req: NextRequest) {
  const origin = process.env.API_ORIGIN || "http://localhost:8080";
  return NextResponse.rewrite(
    new URL(req.nextUrl.pathname + req.nextUrl.search, origin),
  );
}

export const config = {
  matcher: "/api/:path*",
};
