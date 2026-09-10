import path from "node:path";
import { defineConfig } from "vitest/config";

// Unit tests for the pure lib modules. Socket/hook behavior is exercised by
// the Docker E2E verification rather than jsdom; the hook stays thin over
// lib/live.ts, which is fully covered here.
export default defineConfig({
  test: {
    environment: "node",
    include: ["lib/**/*.test.ts"],
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
    },
  },
});
