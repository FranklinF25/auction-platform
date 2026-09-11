import Link from "next/link";
import type { Auction } from "@/lib/api";
import { isSold } from "@/lib/dashboard";
import { formatUSD } from "@/lib/money";
import Countdown from "./Countdown";
import StatusBadge from "./StatusBadge";

// Deterministic gradient placeholders (image upload is a P2/M4 concern).
const gradients = [
  "from-indigo-500 to-violet-600",
  "from-emerald-500 to-teal-600",
  "from-amber-500 to-orange-600",
  "from-sky-500 to-blue-600",
  "from-rose-500 to-pink-600",
  "from-fuchsia-500 to-purple-600",
];

const closedAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

function gradientFor(id: string): string {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) >>> 0;
  }
  return gradients[hash % gradients.length];
}

export default function AuctionCard({ auction }: { auction: Auction }) {
  return (
    <Link
      href={`/auctions/${auction.id}`}
      className="group flex flex-col overflow-hidden rounded-xl border border-zinc-200 bg-white shadow-sm transition hover:-translate-y-0.5 hover:shadow-md"
    >
      <div
        className={`flex h-36 items-center justify-center bg-gradient-to-br ${gradientFor(auction.id)} px-6`}
      >
        <span className="line-clamp-2 text-center text-sm font-semibold text-white/90">
          {auction.title}
        </span>
      </div>

      <div className="flex flex-1 flex-col gap-3 p-4">
        <div className="flex items-start justify-between gap-2">
          <h3 className="line-clamp-1 font-semibold text-zinc-900 group-hover:text-indigo-700">
            {auction.title}
          </h3>
          <StatusBadge status={auction.status} />
        </div>

        <div className="flex items-end justify-between">
          <div>
            <p className="text-xs text-zinc-500">Current price</p>
            <p className="text-lg font-bold tabular-nums text-zinc-900">
              {formatUSD(auction.current_price_cents)}
            </p>
          </div>
          <p className="pb-0.5 text-xs text-zinc-500">
            {auction.bid_count} {auction.bid_count === 1 ? "bid" : "bids"}
          </p>
        </div>

        <div className="mt-auto flex items-center justify-between border-t border-zinc-100 pt-2 text-xs text-zinc-500">
          {auction.status === "active" ? (
            <>
              <span>Ends in</span>
              <Countdown endsAt={auction.ends_at} />
            </>
          ) : auction.status === "closed" ? (
            <>
              <span suppressHydrationWarning>
                Closed {closedAtFmt.format(new Date(auction.closed_at ?? auction.ends_at))}
              </span>
              <span
                className={`font-semibold ${
                  isSold(auction) ? "text-emerald-600" : "text-zinc-400"
                }`}
              >
                {isSold(auction) ? "Sold" : "Unsold"}
              </span>
            </>
          ) : (
            <span>Cancelled</span>
          )}
        </div>
      </div>
    </Link>
  );
}
