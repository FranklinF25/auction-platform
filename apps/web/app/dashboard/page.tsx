"use client";

// Seller dashboard (M3): the signed-in user's own auctions with client-side
// outcome stat chips, status tabs, and cards that drill into the detail page
// (which already carries the full bid history). Revenue stats are deferred to
// the M4 checkout milestone. Auth follows the /sell pattern: guests are bounced
// to /login, other failures stay visible.

import { useQuery } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import AuctionCard from "@/components/AuctionCard";
import { CardGridSkeleton, CardSkeleton, EmptyState, ErrorState } from "@/components/States";
import { ApiError, api } from "@/lib/api";
import {
  computeSellerStats,
  filterBySellerTab,
  type SellerStats,
  type SellerTab,
} from "@/lib/dashboard";

const TABS: { value: SellerTab; label: string }[] = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "sold", label: "Sold" },
  { value: "unsold", label: "Unsold" },
  { value: "cancelled", label: "Cancelled" },
];

const CHIPS: { key: keyof SellerStats; label: string; className: string }[] = [
  { key: "active", label: "Active", className: "bg-emerald-100 text-emerald-700" },
  { key: "sold", label: "Sold", className: "bg-indigo-100 text-indigo-700" },
  { key: "unsold", label: "Unsold", className: "bg-amber-100 text-amber-700" },
  { key: "cancelled", label: "Cancelled", className: "bg-rose-100 text-rose-700" },
];

export default function DashboardPage() {
  const router = useRouter();

  const meQuery = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: false,
  });

  useEffect(() => {
    if (
      meQuery.isError &&
      meQuery.error instanceof ApiError &&
      meQuery.error.status === 401
    ) {
      router.replace("/login");
    }
  }, [meQuery.isError, meQuery.error, router]);

  const auctionsQuery = useQuery({
    queryKey: ["my-auctions"],
    queryFn: () => api.listMyAuctions({ page_size: 50 }),
    enabled: meQuery.data != null,
  });

  const [tab, setTab] = useState<SellerTab>("all");

  if (meQuery.isPending) {
    return (
      <div className="mx-auto max-w-2xl">
        <CardSkeleton />
      </div>
    );
  }
  if (meQuery.isError) {
    return (
      <div className="mx-auto max-w-2xl">
        <ErrorState message={meQuery.error.message} />
      </div>
    );
  }

  const items = auctionsQuery.data?.items ?? [];
  const stats = computeSellerStats(items);
  const visible = filterBySellerTab(items, tab);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-zinc-900 sm:text-3xl">
          Seller dashboard
        </h1>
        <p className="mt-1 text-sm text-zinc-500">
          Signed in as <span className="font-medium">{meQuery.data.name}</span> —
          your listings and their outcomes.
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {CHIPS.map((chip) => (
          <span
            key={chip.key}
            className={`inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-semibold ${chip.className}`}
          >
            <span className="tabular-nums">{stats[chip.key]}</span>
            {chip.label}
          </span>
        ))}
      </div>

      <div
        role="tablist"
        aria-label="Filter your auctions by outcome"
        className="flex w-fit rounded-lg border border-zinc-200 bg-white p-1 shadow-sm"
      >
        {TABS.map((t) => (
          <button
            key={t.label}
            type="button"
            role="tab"
            aria-selected={tab === t.value}
            onClick={() => setTab(t.value)}
            className={`rounded-md px-3 py-1.5 text-sm font-medium transition ${
              tab === t.value
                ? "bg-indigo-600 text-white shadow-sm"
                : "text-zinc-600 hover:bg-zinc-100"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {auctionsQuery.isPending ? (
        <CardGridSkeleton count={6} />
      ) : auctionsQuery.isError ? (
        <ErrorState
          message={auctionsQuery.error.message}
          onRetry={() => auctionsQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title="No auctions yet"
          hint="Put your first item on the block with the Create auction button."
        />
      ) : visible.length === 0 ? (
        <EmptyState
          title="Nothing in this tab"
          hint="No auctions match this outcome right now."
        />
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {visible.map((auction) => (
              <AuctionCard key={auction.id} auction={auction} />
            ))}
          </div>
          <p className="text-xs text-zinc-500">
            Showing {visible.length} of {auctionsQuery.data.total}{" "}
            {auctionsQuery.data.total === 1 ? "auction" : "auctions"}
          </p>
        </>
      )}
    </div>
  );
}
