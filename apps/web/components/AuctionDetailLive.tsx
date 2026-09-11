"use client";

// Client wrapper that hydrates the server-rendered auction with live state: one
// socket (useAuctionSocket), a server-authoritative countdown, live price,
// watcher count, and the bid form. The static hero and description arrive as
// server-rendered children for first paint + SEO; everything here renders from
// the socket projection after hydration.

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useEffect, useState, type ReactNode } from "react";
import { api, type Auction } from "@/lib/api";
import { formatUSD } from "@/lib/money";
import { useAuctionSocket, type SocketStatus } from "@/lib/useAuctionSocket";
import BidForm from "./BidForm";
import BidHistory from "./BidHistory";
import Countdown from "./Countdown";
import OutcomeBanner from "./OutcomeBanner";
import StatusBadge from "./StatusBadge";
import { primaryButtonClass } from "./formStyles";

const closedAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

/** Connection pill, top-right of the card: green when the feed is live. */
function LivePill({ status }: { status: SocketStatus }) {
  if (status === "open") {
    return (
      <span className="absolute right-4 top-4 inline-flex items-center gap-1.5 text-[11px] font-medium text-zinc-500">
        <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
        Live
      </span>
    );
  }
  if (status === "closed") {
    return (
      <span className="absolute right-4 top-4 inline-flex items-center gap-1.5 text-[11px] font-medium text-zinc-400">
        <span className="h-1.5 w-1.5 rounded-full bg-zinc-300" />
        Feed ended
      </span>
    );
  }
  const label = status === "connecting" ? "Connecting" : "Reconnecting";
  return (
    <span className="absolute right-4 top-4 inline-flex items-center gap-1.5 text-[11px] font-medium text-zinc-500">
      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500" />
      {label}…
    </span>
  );
}

/** "+ extended" flash: brief amber badge when the soft close moves ends_at. */
function ExtendedFlash({ at }: { at: number | null }) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (at === null) return;
    setVisible(true);
    const t = setTimeout(() => setVisible(false), 2400);
    return () => clearTimeout(t);
  }, [at]);

  if (at === null || !visible) return null;
  return (
    <span className="animate-extended-flash rounded-full bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-700">
      + extended
    </span>
  );
}

/** "N watching" chip, fed by presence.update. */
function WatchersChip({ watchers }: { watchers: number }) {
  return (
    <span
      className="inline-flex items-center gap-1.5 text-xs text-zinc-500"
      title="Browsers watching this auction right now"
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="h-3.5 w-3.5"
        aria-hidden
      >
        <path d="M2.25 12s3.5-7 9.75-7 9.75 7 9.75 7-3.5 7-9.75 7-9.75-7-9.75-7Z" />
        <circle cx="12" cy="12" r="3" />
      </svg>
      {watchers} watching
    </span>
  );
}

export default function AuctionDetailLive({
  auction,
  children,
}: {
  auction: Auction;
  children: ReactNode;
}) {
  const live = useAuctionSocket(auction.id, {
    status: auction.status,
    currentPriceCents: auction.current_price_cents,
    bidCount: auction.bid_count,
    endsAt: auction.ends_at,
    reserveMet: auction.reserve_met,
    closed:
      auction.status === "closed"
        ? {
            // Per the API contract, winner_name is set iff closed and sold.
            winnerName: auction.winner_name,
            sold: auction.winner_name !== null,
            finalPriceCents: auction.current_price_cents,
          }
        : null,
  });

  // Same query the Header runs — TanStack dedupes to a single request. A 401
  // here is the expected guest state, not a failure: no retries.
  const meQuery = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: false,
  });
  const me = meQuery.data ?? null;
  const isSeller = me != null && me.id === auction.seller_id;
  const active = live.auctionStatus === "active";

  // Winner view: the live closed event once it lands, else the SSR seed. The
  // server-rendered fetch is unauthenticated, so you_won is false there; for a
  // signed-in viewer the display-name match against me lights the banner both
  // on load and when the close happens mid-session.
  const winnerName =
    live.closed?.winnerName ??
    (auction.status === "closed" ? auction.winner_name : null);
  const youWon =
    auction.you_won ||
    (winnerName != null && me != null && me.name === winnerName);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-bold tracking-tight text-zinc-900 sm:text-3xl">
          {auction.title}
        </h1>
        <StatusBadge status={live.auctionStatus} />
      </div>

      <OutcomeBanner
        status={live.auctionStatus}
        bidCount={live.bidCount}
        reserveMet={live.reserveMet}
        currentPriceCents={live.currentPriceCents}
        winnerName={winnerName}
      />

      {youWon ? (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-xl border border-emerald-300 bg-emerald-50 px-4 py-3 text-sm font-medium text-emerald-800">
          <span>You won this auction — complete your purchase.</span>
          <Link
            href="/purchases"
            className="rounded-lg bg-emerald-600 px-3 py-1.5 font-semibold text-white shadow-sm transition hover:bg-emerald-500"
          >
            Complete purchase
          </Link>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="flex flex-col gap-6 lg:col-span-2">
          {children}
          <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
            <h2 className="font-semibold text-zinc-900">Bid history</h2>
            <div className="mt-2">
              <BidHistory auctionId={auction.id} liveBids={live.bids} />
            </div>
          </section>
        </div>

        <aside className="relative flex flex-col gap-4 rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm lg:sticky lg:top-20 lg:self-start">
          {active ? <LivePill status={live.status} /> : null}

          <div>
            <p className="text-xs text-zinc-500">Current price</p>
            {/* key on the price: each accepted bid remounts the element and
                replays the flash animation. */}
            <p
              key={live.currentPriceCents}
              className="animate-price-flash origin-left text-3xl font-bold tabular-nums text-zinc-900"
            >
              {formatUSD(live.currentPriceCents)}
            </p>
            <p className="mt-1 text-xs text-zinc-500">
              {live.bidCount} {live.bidCount === 1 ? "bid" : "bids"} so far
            </p>
          </div>

          <div className="flex items-center justify-between rounded-xl bg-zinc-50 px-3 py-2.5">
            <span className="text-sm text-zinc-600">
              {active ? "Ends in" : "Ended"}
            </span>
            <span className="flex items-center gap-2">
              <ExtendedFlash at={live.extendedAt} />
              <Countdown
                endsAt={live.endsAt}
                offsetMs={live.offsetMs ?? 0}
                className="text-base"
              />
            </span>
          </div>
          <div className="-mt-2 flex items-center justify-between gap-2">
            <p className="text-xs text-zinc-400">
              Closes {closedAtFmt.format(new Date(live.endsAt))}
            </p>
            {live.watchers !== null ? (
              <WatchersChip watchers={live.watchers} />
            ) : null}
          </div>

          {live.auctionStatus !== "cancelled" ? (
            <span
              className={`inline-flex w-fit rounded-full px-2.5 py-0.5 text-xs font-semibold ${
                live.reserveMet
                  ? "bg-emerald-100 text-emerald-700"
                  : "bg-amber-100 text-amber-700"
              }`}
            >
              {live.reserveMet ? "Reserve met" : "Reserve not met"}
            </span>
          ) : null}

          <dl className="flex flex-col gap-1.5 border-t border-zinc-100 pt-3 text-sm">
            <div className="flex justify-between">
              <dt className="text-zinc-500">Starting price</dt>
              <dd className="font-medium tabular-nums text-zinc-800">
                {formatUSD(auction.starting_price_cents)}
              </dd>
            </div>
            <div className="flex justify-between">
              <dt className="text-zinc-500">Min increment</dt>
              <dd className="font-medium tabular-nums text-zinc-800">
                {formatUSD(auction.min_increment_cents)}
              </dd>
            </div>
          </dl>

          {!active ? null : meQuery.isPending ? (
            <div className="h-28 animate-pulse rounded-xl bg-zinc-100" />
          ) : me === null ? (
            <div className="flex flex-col items-center gap-2.5 rounded-xl border border-zinc-200 bg-zinc-50 px-4 py-4 text-center">
              <p className="text-sm font-medium text-zinc-700">
                Sign in to bid on this auction
              </p>
              <Link href="/login" className={`${primaryButtonClass} w-full`}>
                Sign in
              </Link>
              <p className="text-xs text-zinc-500">
                New to Subasta?{" "}
                <Link
                  href="/register"
                  className="font-medium text-indigo-600 hover:underline"
                >
                  Create an account
                </Link>
              </p>
            </div>
          ) : isSeller ? (
            <div className="rounded-xl border border-zinc-200 bg-zinc-50 px-4 py-3 text-sm text-zinc-600">
              This is your auction — sellers can&apos;t bid on their own
              listings.
            </div>
          ) : (
            <BidForm
              auctionId={auction.id}
              currentPriceCents={live.currentPriceCents}
              minIncrementCents={auction.min_increment_cents}
              me={me}
              onPlaced={live.applyOwnBid}
            />
          )}
        </aside>
      </div>
    </div>
  );
}
