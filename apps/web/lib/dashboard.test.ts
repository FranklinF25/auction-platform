import { describe, expect, it } from "vitest";
import type { Auction, AuctionStatus } from "@/lib/api";
import {
  computeSellerStats,
  filterBySellerTab,
  isSold,
  type SellerTab,
} from "@/lib/dashboard";

let seq = 0;

function item(status: AuctionStatus, over: Partial<Auction> = {}): Auction {
  seq += 1;
  return {
    id: `a${seq}`,
    seller_id: "u1",
    title: `Item ${seq}`,
    description: "",
    starting_price_cents: 1000,
    min_increment_cents: 100,
    current_price_cents: 1000,
    reserve_met: false,
    bid_count: 0,
    status,
    ends_at: "2026-07-01T00:00:00Z",
    closed_at: null,
    created_at: "2026-06-01T00:00:00Z",
    winner_name: null,
    you_won: false,
    ...over,
  };
}

describe("isSold", () => {
  it("is true only for closed auctions with a reserve-meeting bid", () => {
    expect(isSold(item("closed", { bid_count: 2, reserve_met: true }))).toBe(
      true,
    );
  });

  it("is false without bids, without reserve, or while not closed", () => {
    expect(isSold(item("closed", { bid_count: 0, reserve_met: false }))).toBe(
      false,
    );
    expect(isSold(item("closed", { bid_count: 3, reserve_met: false }))).toBe(
      false,
    );
    expect(isSold(item("active", { bid_count: 5, reserve_met: true }))).toBe(
      false,
    );
    expect(isSold(item("cancelled", { bid_count: 1, reserve_met: true }))).toBe(
      false,
    );
  });
});

describe("computeSellerStats", () => {
  it("counts each outcome bucket", () => {
    const stats = computeSellerStats([
      item("active"),
      item("active"),
      item("closed", { bid_count: 1, reserve_met: true }),
      item("closed", { bid_count: 0 }),
      item("closed", { bid_count: 2, reserve_met: false }),
      item("cancelled"),
    ]);
    expect(stats).toEqual({ active: 2, sold: 1, unsold: 2, cancelled: 1 });
  });

  it("returns zeroed stats for an empty list", () => {
    expect(computeSellerStats([])).toEqual({
      active: 0,
      sold: 0,
      unsold: 0,
      cancelled: 0,
    });
  });
});

describe("filterBySellerTab", () => {
  const items = [
    item("active"),
    item("closed", { bid_count: 1, reserve_met: true }),
    item("closed", { bid_count: 0 }),
    item("cancelled"),
  ];

  it.each<[SellerTab, number]>([
    ["all", 4],
    ["active", 1],
    ["sold", 1],
    ["unsold", 1],
    ["cancelled", 1],
  ])("tab %s keeps %i auction(s)", (tab, expected) => {
    expect(filterBySellerTab(items, tab)).toHaveLength(expected);
  });

  it("keeps the original order and items", () => {
    expect(filterBySellerTab(items, "all")).toEqual(items);
  });
});
