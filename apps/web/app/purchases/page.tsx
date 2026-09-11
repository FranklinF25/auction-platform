"use client";

// Buyer dashboard (M3): auctions the signed-in user won, newest close first.
// Checkout is simulated in this build — the disabled CTA marks where the M4
// payment flow lands. Auth follows the /sell pattern: guests are bounced to
// /login, other failures stay visible.

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { primaryButtonClass } from "@/components/formStyles";
import { EmptyState, ErrorState } from "@/components/States";
import { ApiError, api, type Auction } from "@/lib/api";
import { formatUSD } from "@/lib/money";

const wonAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

function RowSkeleton() {
  return <div className="h-28 animate-pulse rounded-2xl border border-zinc-200 bg-white" />;
}

function PurchaseRow({ purchase }: { purchase: Auction }) {
  return (
    <div className="flex flex-col gap-4 rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0">
        <Link
          href={`/auctions/${purchase.id}`}
          className="font-semibold text-zinc-900 hover:text-indigo-700 hover:underline"
        >
          {purchase.title}
        </Link>
        <p className="mt-1 text-xs text-zinc-500">
          <span suppressHydrationWarning>
            Won {wonAtFmt.format(new Date(purchase.closed_at ?? purchase.ends_at))}
          </span>{" "}
          · {purchase.bid_count} {purchase.bid_count === 1 ? "bid" : "bids"}
        </p>
      </div>

      <div className="flex flex-col items-start gap-2 sm:items-end">
        <p className="text-lg font-bold tabular-nums text-zinc-900">
          {formatUSD(purchase.current_price_cents)}
        </p>
        <button
          type="button"
          disabled
          title="Simulated checkout ships in M4"
          className={primaryButtonClass}
        >
          Complete purchase
        </button>
        <p className="text-xs text-zinc-400">Simulated checkout ships in M4.</p>
      </div>
    </div>
  );
}

export default function PurchasesPage() {
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

  const purchasesQuery = useQuery({
    queryKey: ["my-purchases"],
    queryFn: () => api.listMyPurchases({ page_size: 50 }),
    enabled: meQuery.data != null,
  });

  if (meQuery.isPending) {
    return (
      <div className="mx-auto flex max-w-3xl flex-col gap-3">
        <RowSkeleton />
        <RowSkeleton />
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

  const purchases = purchasesQuery.data?.items ?? [];

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-5">
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-zinc-900 sm:text-3xl">
          Your purchases
        </h1>
        <p className="mt-1 text-sm text-zinc-500">
          Auctions you won — complete each purchase to claim your item.
        </p>
      </div>

      {purchasesQuery.isPending ? (
        <div className="flex flex-col gap-3">
          <RowSkeleton />
          <RowSkeleton />
          <RowSkeleton />
        </div>
      ) : purchasesQuery.isError ? (
        <ErrorState
          message={purchasesQuery.error.message}
          onRetry={() => purchasesQuery.refetch()}
        />
      ) : purchases.length === 0 ? (
        <EmptyState
          title="No purchases yet"
          hint="Auctions you win will show up here the moment they close."
        />
      ) : (
        <>
          <div className="flex flex-col gap-3">
            {purchases.map((purchase) => (
              <PurchaseRow key={purchase.id} purchase={purchase} />
            ))}
          </div>
          <p className="text-xs text-zinc-500">
            Showing {purchases.length} of {purchasesQuery.data.total}{" "}
            {purchasesQuery.data.total === 1 ? "purchase" : "purchases"}
          </p>
        </>
      )}
    </div>
  );
}
