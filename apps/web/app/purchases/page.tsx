"use client";

// Buyer dashboard (M4): auctions the signed-in user won, newest close first,
// each joined with its checkout transaction. The row action is state-aware:
// pending offers checkout, failed retries it, completed shows the paid chip,
// expired means the sale is lost. Auth follows the /sell pattern: guests are
// bounced to /login, other failures stay visible.

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { primaryButtonClass } from "@/components/formStyles";
import { EmptyState, ErrorState } from "@/components/States";
import { ApiError, api, type Purchase } from "@/lib/api";
import { formatUSD } from "@/lib/money";

const wonAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

const paidAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

function RowSkeleton() {
  return <div className="h-28 animate-pulse rounded-2xl border border-zinc-200 bg-white" />;
}

// The per-purchase action, derived from the transaction status: pending ->
// checkout CTA, failed -> retry CTA plus a muted note, completed -> emerald
// paid chip with the payment date, expired -> muted lost note.
function PurchaseAction({ purchase }: { purchase: Purchase }) {
  const { transaction } = purchase;

  if (transaction.status === "completed") {
    return (
      <div className="flex flex-col items-start gap-2 sm:items-end">
        <span className="inline-flex items-center gap-1.5 rounded-full bg-emerald-100 px-3 py-1 text-xs font-semibold text-emerald-700">
          Paid
        </span>
        {transaction.paid_at ? (
          <p className="text-xs text-zinc-500">
            <span suppressHydrationWarning>
              Paid {paidAtFmt.format(new Date(transaction.paid_at))}
            </span>
          </p>
        ) : null}
      </div>
    );
  }

  if (transaction.status === "expired") {
    return <p className="text-xs font-medium text-zinc-400">Expired — sale lost</p>;
  }

  const failed = transaction.status === "failed";
  return (
    <div className="flex flex-col items-start gap-2 sm:items-end">
      <Link href={`/checkout/${transaction.id}`} className={primaryButtonClass}>
        {failed ? "Retry payment" : "Complete purchase"}
      </Link>
      {failed ? <p className="text-xs text-zinc-500">Payment failed</p> : null}
    </div>
  );
}

function PurchaseRow({ purchase }: { purchase: Purchase }) {
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
        <PurchaseAction purchase={purchase} />
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
