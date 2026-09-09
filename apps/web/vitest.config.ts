import path from "node:path";
import { defineConfig } from "vitest/config";

// Unit tests for the pure lib modules only; component smoke tests arrive in
// M2 together with the live-bidding milestone.
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
