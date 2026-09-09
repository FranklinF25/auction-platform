"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { formatUSD } from "@/lib/money";
import { EmptyState, ErrorState } from "./States";

const timeFmt = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
});

export default function BidHistory({ auctionId }: { auctionId: string }) {
  const { data, isPending, isError, error, refetch } = useQuery({
    queryKey: ["bids", auctionId],
    queryFn: () => api.listBids(auctionId, { page_size: 50 }),
  });

  if (isPending) {
    return (
      <div className="flex flex-col gap-3 py-2">
        {Array.from({ length: 3 }, (_, i) => (
          <div key={i} className="h-10 animate-pulse rounded-lg bg-zinc-100" />
        ))}
      </div>
    );
  }
  if (isError) {
    return <ErrorState message={error.message} onRetry={() => refetch()} />;
  }
  if (data.items.length === 0) {
    return (
      <EmptyState
        title="No bids yet"
        hint="Live bidding arrives in M2 — be ready."
      />
    );
  }

  // The API already returns bids newest-first; sort defensively anyway so the
  // presentation contract holds regardless of backend ordering details.
  const bids = [...data.items].sort((a, b) =>
    b.created_at.localeCompare(a.created_at),
  );

  return (
    <div>
      <ol className="divide-y divide-zinc-100">
        {bids.map((bid, index) => (
          <li
            key={bid.id}
            className="flex items-center justify-between gap-3 py-3"
          >
            <span className="flex min-w-0 items-center gap-2">
              {index === 0 ? (
                <span className="shrink-0 rounded-full bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-700">
                  Highest
                </span>
              ) : null}
              <span className="truncate font-medium text-zinc-800">
                {bid.bidder_name}
              </span>
            </span>
            <span className="flex shrink-0 items-center gap-3 text-sm">
              <span className="hidden text-zinc-500 sm:inline">
                {timeFmt.format(new Date(bid.created_at))}
              </span>
              <span className="font-semibold tabular-nums text-zinc-900">
                {formatUSD(bid.amount_cents)}
              </span>
            </span>
          </li>
        ))}
      </ol>
      {data.total > bids.length ? (
        <p className="pt-3 text-xs text-zinc-500">
          Showing the newest {bids.length} of {data.total} bids.
        </p>
      ) : null}
    </div>
  );
}
