// Pure seller-dashboard helpers: outcome classification, stat counts, and tab
// filtering computed client-side over list items (the M3 endpoint serves the
// same ListItem shape as GET /api/auctions). Revenue stats are deferred to the
// M4 checkout milestone.

import type { Auction } from "@/lib/api";

/** Outcome buckets shown as stat chips on the dashboard. */
export interface SellerStats {
  active: number;
  sold: number;
  unsold: number;
  cancelled: number;
}

/** True when a closed auction found a winning bidder at or above the reserve. */
export function isSold(
  auction: Pick<Auction, "status" | "bid_count" | "reserve_met">,
): boolean {
  return auction.status === "closed" && auction.bid_count > 0 && auction.reserve_met;
}

/** Counts each outcome bucket over a list of the seller's auctions. */
export function computeSellerStats(
  items: Pick<Auction, "status" | "bid_count" | "reserve_met">[],
): SellerStats {
  const stats: SellerStats = { active: 0, sold: 0, unsold: 0, cancelled: 0 };
  for (const item of items) {
    if (item.status === "active") stats.active += 1;
    else if (item.status === "cancelled") stats.cancelled += 1;
    else if (isSold(item)) stats.sold += 1;
    else stats.unsold += 1;
  }
  return stats;
}

/** Dashboard tabs; "closed" splits into its sold/unsold outcomes. */
export type SellerTab = "all" | "active" | "sold" | "unsold" | "cancelled";

/** Filters the seller's auctions for the selected dashboard tab. */
export function filterBySellerTab(items: Auction[], tab: SellerTab): Auction[] {
  switch (tab) {
    case "all":
      return items;
    case "active":
      return items.filter((a) => a.status === "active");
    case "sold":
      return items.filter(isSold);
    case "unsold":
      return items.filter((a) => a.status === "closed" && !isSold(a));
    case "cancelled":
      return items.filter((a) => a.status === "cancelled");
  }
}
