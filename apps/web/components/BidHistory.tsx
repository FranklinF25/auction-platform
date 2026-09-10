"use client";

// Bid history: the newest 50 bids from REST (TanStack) merged with bids that
// arrived live over the socket while this page was open. Merge is deduped by
// bid id — our own POST responses and their bid.placed echoes collapse into
// one row — and always rendered newest first.

import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { api } from "@/lib/api";
import { formatUSD } from "@/lib/money";
import type { LiveBid } from "@/lib/useAuctionSocket";
import { EmptyState, ErrorState } from "./States";

const timeFmt = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
});

/** One merged row, normalized from either a fetched Bid or a LiveBid. */
interface HistoryRow {
  id: string;
  bidderName: string;
  amountCents: number;
  createdAt: string;
  own: boolean;
  /** Arrived over the socket this session → animate its entrance. */
  fresh: boolean;
}

export default function BidHistory({
  auctionId,
  liveBids = [],
}: {
  auctionId: string;
  liveBids?: LiveBid[];
}) {
  const { data, isPending, isError, error, refetch } = useQuery({
    queryKey: ["bids", auctionId],
    queryFn: () => api.listBids(auctionId, { page_size: 50 }),
  });

  const rows = useMemo<HistoryRow[]>(() => {
    const liveById = new Map(liveBids.map((b) => [b.id, b]));
    // Live rows win the dedupe; fetched copies of the same bid are dropped so
    // a refetch never duplicates a row we already prepended.
    const fetched: HistoryRow[] = (data?.items ?? [])
      .filter((b) => !liveById.has(b.id))
      .map((b) => ({
        id: b.id,
        bidderName: b.bidder_name,
        amountCents: b.amount_cents,
        createdAt: b.created_at,
        own: false,
        fresh: false,
      }));
    const live: HistoryRow[] = liveBids.map((b) => ({
      id: b.id,
      bidderName: b.bidderName,
      amountCents: b.amountCents,
      createdAt: b.createdAt,
      own: b.own,
      fresh: true,
    }));
    // The presentation contract is newest-first regardless of source order.
    return [...live, ...fetched].sort((a, b) =>
      b.createdAt.localeCompare(a.createdAt),
    );
  }, [data, liveBids]);

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
  if (rows.length === 0) {
    return (
      <EmptyState title="No bids yet" hint="Be the first — bidding is live." />
    );
  }

  return (
    <div>
      <ol className="divide-y divide-zinc-100">
        {rows.map((bid, index) => (
          <li
            key={bid.id}
            className={`flex items-center justify-between gap-3 py-3 ${bid.fresh ? "animate-bid-in" : ""}`}
          >
            <span className="flex min-w-0 items-center gap-2">
              {index === 0 ? (
                <span className="shrink-0 rounded-full bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-700">
                  Highest
                </span>
              ) : null}
              <span className="truncate font-medium text-zinc-800">
                {bid.bidderName}
              </span>
              {bid.own ? (
                <span className="shrink-0 rounded-full bg-indigo-100 px-2 py-0.5 text-xs font-semibold text-indigo-700">
                  You
                </span>
              ) : null}
            </span>
            <span className="flex shrink-0 items-center gap-3 text-sm">
              <span className="hidden text-zinc-500 sm:inline">
                {timeFmt.format(new Date(bid.createdAt))}
              </span>
              <span className="font-semibold tabular-nums text-zinc-900">
                {formatUSD(bid.amountCents)}
              </span>
            </span>
          </li>
        ))}
      </ol>
      {data && data.total > rows.length ? (
        <p className="pt-3 text-xs text-zinc-500">
          Showing the newest {rows.length} of {data.total} bids.
        </p>
      ) : null}
    </div>
  );
}
