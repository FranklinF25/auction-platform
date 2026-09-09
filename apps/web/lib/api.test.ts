import { describe, expect, it } from "vitest";
import { buildUrl } from "@/lib/api";

describe("buildUrl", () => {
  it("returns the bare path when there are no params", () => {
    expect(buildUrl("/api/auctions")).toBe("/api/auctions");
    expect(buildUrl("/api/auctions", {})).toBe("/api/auctions");
  });

  it("appends numeric params in insertion order", () => {
    expect(buildUrl("/api/auctions", { page: 2, page_size: 10 })).toBe(
      "/api/auctions?page=2&page_size=10",
    );
  });

  it("omits undefined and empty-string params", () => {
    expect(
      buildUrl("/api/auctions", { status: undefined, q: "", page: 1 }),
    ).toBe("/api/auctions?page=1");
  });

  it("encodes search terms", () => {
    expect(buildUrl("/api/auctions", { q: "vintage guitar" })).toBe(
      "/api/auctions?q=vintage+guitar",
    );
    expect(buildUrl("/api/auctions", { q: "50% off" })).toBe(
      "/api/auctions?q=50%25+off",
    );
  });

  it("builds nested resource paths", () => {
    expect(buildUrl("/api/auctions/abc-123/bids", { page_size: 50 })).toBe(
      "/api/auctions/abc-123/bids?page_size=50",
    );
  });
});
