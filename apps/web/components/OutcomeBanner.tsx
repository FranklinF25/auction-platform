import type { AuctionStatus } from "@/lib/api";
import { formatUSD } from "@/lib/money";

// Final-state banner for non-active auctions (read-only outcome per the PRD).
// Rendered from live state so an auction that ends mid-session flips in place.
export default function OutcomeBanner({
  status,
  bidCount,
  reserveMet,
  currentPriceCents,
}: {
  status: AuctionStatus;
  bidCount: number;
  reserveMet: boolean;
  currentPriceCents: number;
}) {
  if (status === "cancelled") {
    return (
      <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm font-medium text-rose-700">
        This auction was cancelled by the seller.
      </div>
    );
  }
  if (status !== "closed") return null;

  const message =
    bidCount === 0
      ? "This auction closed with no bids."
      : reserveMet
        ? `Sold for ${formatUSD(currentPriceCents)}.`
        : "Closed below the seller's reserve — unsold.";

  return (
    <div className="rounded-xl border border-indigo-200 bg-indigo-50 px-4 py-3 text-sm font-medium text-indigo-800">
      {message}
    </div>
  );
}
