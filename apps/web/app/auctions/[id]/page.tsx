import { notFound } from "next/navigation";
import BidHistory from "@/components/BidHistory";
import Countdown from "@/components/Countdown";
import StatusBadge from "@/components/StatusBadge";
import { ApiError, api, type Auction } from "@/lib/api";
import { formatUSD } from "@/lib/money";

const gradients = [
  "from-indigo-500 to-violet-600",
  "from-emerald-500 to-teal-600",
  "from-amber-500 to-orange-600",
  "from-sky-500 to-blue-600",
  "from-rose-500 to-pink-600",
  "from-fuchsia-500 to-purple-600",
];

function gradientFor(id: string): string {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) >>> 0;
  }
  return gradients[hash % gradients.length];
}

function closedAtFmt(endsAt: string): string {
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(endsAt));
}

function OutcomeBanner({ auction }: { auction: Auction }) {
  if (auction.status === "cancelled") {
    return (
      <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm font-medium text-rose-700">
        This auction was cancelled by the seller.
      </div>
    );
  }
  if (auction.status !== "closed") return null;

  const message =
    auction.bid_count === 0
      ? "This auction closed with no bids."
      : auction.reserve_met
        ? `Sold for ${formatUSD(auction.current_price_cents)}.`
        : "Closed below the seller's reserve — unsold.";

  return (
    <div className="rounded-xl border border-indigo-200 bg-indigo-50 px-4 py-3 text-sm font-medium text-indigo-800">
      {message}
    </div>
  );
}

export default async function AuctionDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  let auction: Auction;
  try {
    // Server-side fetch goes straight to the API origin (the /api proxy
    // middleware only applies to browser requests); see lib/api.ts.
    auction = await api.getAuction(id);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      notFound();
    }
    throw err;
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-bold tracking-tight text-zinc-900 sm:text-3xl">
          {auction.title}
        </h1>
        <StatusBadge status={auction.status} />
      </div>

      <OutcomeBanner auction={auction} />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="flex flex-col gap-6 lg:col-span-2">
          <div
            className={`flex h-64 items-center justify-center rounded-2xl bg-gradient-to-br ${gradientFor(auction.id)} px-8`}
          >
            <span className="text-center text-lg font-semibold text-white/90">
              {auction.title}
            </span>
          </div>

          <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
            <h2 className="font-semibold text-zinc-900">Description</h2>
            {auction.description ? (
              <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-600">
                {auction.description}
              </p>
            ) : (
              <p className="mt-2 text-sm italic text-zinc-400">
                No description provided.
              </p>
            )}
          </section>

          <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
            <h2 className="font-semibold text-zinc-900">Bid history</h2>
            <div className="mt-2">
              <BidHistory auctionId={auction.id} />
            </div>
          </section>
        </div>

        <aside className="flex flex-col gap-4 rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm lg:sticky lg:top-20 lg:self-start">
          <div>
            <p className="text-xs text-zinc-500">Current price</p>
            <p className="text-3xl font-bold tabular-nums text-zinc-900">
              {formatUSD(auction.current_price_cents)}
            </p>
            <p className="mt-1 text-xs text-zinc-500">
              {auction.bid_count} {auction.bid_count === 1 ? "bid" : "bids"} so
              far
            </p>
          </div>

          <div className="flex items-center justify-between rounded-xl bg-zinc-50 px-3 py-2.5">
            <span className="text-sm text-zinc-600">
              {auction.status === "active" ? "Ends in" : "Ended"}
            </span>
            <Countdown endsAt={auction.ends_at} className="text-base" />
          </div>
          <p className="-mt-2 text-xs text-zinc-400">
            Closes {closedAtFmt(auction.ends_at)}
          </p>

          {auction.status !== "cancelled" ? (
            <span
              className={`inline-flex w-fit rounded-full px-2.5 py-0.5 text-xs font-semibold ${
                auction.reserve_met
                  ? "bg-emerald-100 text-emerald-700"
                  : "bg-amber-100 text-amber-700"
              }`}
            >
              {auction.reserve_met ? "Reserve met" : "Reserve not met"}
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

          <button
            type="button"
            disabled
            title="Live bidding lands in milestone 2"
            className="w-full cursor-not-allowed rounded-lg bg-zinc-200 px-4 py-2.5 text-sm font-semibold text-zinc-500"
          >
            Live bidding arrives in M2
          </button>
          <p className="text-center text-xs text-zinc-400">
            This milestone is browse-only; real-time bidding is next.
          </p>
        </aside>
      </div>
    </div>
  );
}
