"use client";

// M4 checkout: the winner's simulated payment for one won auction. The page is
// auth-gated like /dashboard (401 -> /login) and reads the transaction from the
// purchases list — the honest single source for the buyer's checkout state (the
// API exposes no per-transaction GET). The payment itself is a demo: no real
// card is ever charged, and the decline card is documented right in the form,
// per the PRD's "clearly marked as simulation" requirement.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
  fieldErrorClass,
  inputClass,
  labelClass,
  primaryButtonClass,
} from "@/components/formStyles";
import { CardSkeleton, ErrorState } from "@/components/States";
import {
  ApiError,
  api,
  type Purchase,
  type TransactionStatus,
} from "@/lib/api";
import {
  DECLINE_CARD_FORMATTED,
  cardDigits,
  formatCardNumber,
  isValidCardNumber,
} from "@/lib/checkout";
import { formatUSD } from "@/lib/money";

const paidAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

const expiresAtFmt = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "short",
});

const statusStyles: Record<TransactionStatus, { label: string; className: string }> = {
  pending: { label: "Awaiting payment", className: "bg-amber-100 text-amber-700" },
  completed: { label: "Paid", className: "bg-emerald-100 text-emerald-700" },
  failed: { label: "Payment failed", className: "bg-rose-100 text-rose-700" },
  expired: { label: "Expired", className: "bg-zinc-200 text-zinc-700" },
};

function TransactionChip({ status }: { status: TransactionStatus }) {
  const style = statusStyles[status];
  return (
    <span
      className={`shrink-0 rounded-full px-2.5 py-0.5 text-xs font-semibold ${style.className}`}
    >
      {style.label}
    </span>
  );
}

function OrderSummary({
  purchase,
  status,
}: {
  purchase: Purchase;
  status: TransactionStatus;
}) {
  return (
    <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
      <h2 className="font-semibold text-zinc-900">Order summary</h2>
      <div className="mt-3 flex items-start justify-between gap-3">
        <Link
          href={`/auctions/${purchase.id}`}
          className="font-semibold text-zinc-900 hover:text-indigo-700 hover:underline"
        >
          {purchase.title}
        </Link>
        <TransactionChip status={status} />
      </div>
      <div className="mt-4 flex items-center justify-between border-t border-zinc-100 pt-4">
        <span className="text-sm text-zinc-500">Final price</span>
        <span className="text-lg font-bold tabular-nums text-zinc-900">
          {formatUSD(purchase.transaction.amount_cents)}
        </span>
      </div>
    </section>
  );
}

function SuccessPanel({
  purchase,
  paidAt,
}: {
  purchase: Purchase;
  paidAt: string | null;
}) {
  return (
    <section className="rounded-2xl border border-emerald-200 bg-emerald-50 p-8 text-center shadow-sm">
      <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-emerald-100 text-emerald-600">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          className="h-6 w-6"
          aria-hidden="true"
        >
          <path d="M5 13l4 4L19 7" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </div>
      <h2 className="mt-3 text-lg font-bold text-emerald-900">Payment complete</h2>
      <p className="mt-1 text-sm text-emerald-700">
        {formatUSD(purchase.transaction.amount_cents)} paid for {purchase.title}
        {paidAt ? (
          <>
            {" — "}
            <span suppressHydrationWarning>
              paid {paidAtFmt.format(new Date(paidAt))}
            </span>
          </>
        ) : null}
        .
      </p>
      <Link href="/purchases" className={`${primaryButtonClass} mt-5`}>
        Back to purchases
      </Link>
    </section>
  );
}

function ExpiredPanel({ purchase }: { purchase: Purchase }) {
  return (
    <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
      <h2 className="font-semibold text-zinc-900">Payment window closed</h2>
      <p className="mt-1 text-sm text-zinc-500">
        This transaction expired — the sale is lost and can no longer be paid.
        Winners have 48 hours after the auction closes.
      </p>
      <p className="mt-2 text-xs text-zinc-400">
        <span suppressHydrationWarning>
          Window closed {expiresAtFmt.format(new Date(purchase.transaction.expires_at))}
        </span>
      </p>
      <Link href="/purchases" className={`${primaryButtonClass} mt-4`}>
        Back to purchases
      </Link>
    </section>
  );
}

function NotFoundPanel() {
  return (
    <div className="flex flex-col items-center gap-3 rounded-2xl border border-dashed border-zinc-300 bg-white/60 px-6 py-16 text-center">
      <p className="font-semibold text-zinc-900">Purchase not found</p>
      <p className="max-w-md text-sm text-zinc-500">
        This checkout doesn&apos;t match any of your won auctions.
      </p>
      <Link href="/purchases" className={`${primaryButtonClass} mt-1`}>
        Back to purchases
      </Link>
    </div>
  );
}

export default function CheckoutPage() {
  const router = useRouter();
  const params = useParams<{ id: string }>();
  const transactionId = params.id;
  const queryClient = useQueryClient();

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

  // The API serves purchases pages of at most 50 items, so this matches the
  // /purchases view's reach: a transaction older than the 50 most recent wins
  // reads as not found here.
  const purchasesQuery = useQuery({
    queryKey: ["my-purchases"],
    queryFn: () => api.listMyPurchases({ page_size: 50 }),
    enabled: meQuery.data != null,
  });

  // Local overrides from the pay attempt, so the page flips without a refetch.
  const [statusOverride, setStatusOverride] = useState<TransactionStatus | null>(
    null,
  );
  const [paidAtOverride, setPaidAtOverride] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [declined, setDeclined] = useState(false);
  const [card, setCard] = useState("");
  const [cardError, setCardError] = useState<string | null>(null);

  const pay = useMutation({
    mutationFn: (cardNumber: string) =>
      api.payTransaction(transactionId, cardNumber),
    onSuccess: (res) => {
      if (res.status === "completed") {
        setStatusOverride("completed");
        setPaidAtOverride(res.paid_at);
        setDeclined(false);
        // The purchases list drives both this page and /purchases.
        queryClient.invalidateQueries({ queryKey: ["my-purchases"] });
      } else {
        setDeclined(true);
      }
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 404) {
        setNotFound(true);
      } else if (
        err instanceof ApiError &&
        err.code === "transaction_expired"
      ) {
        setStatusOverride("expired");
      }
      // Everything else stays inline under the form via pay.error.
    },
  });

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

  const purchase = purchasesQuery.data?.items.find(
    (p) => p.transaction.id === transactionId,
  );

  if (notFound || (purchasesQuery.isSuccess && purchase == null)) {
    return (
      <div className="mx-auto max-w-2xl">
        <NotFoundPanel />
      </div>
    );
  }

  if (purchasesQuery.isPending) {
    return (
      <div className="mx-auto max-w-2xl">
        <CardSkeleton />
      </div>
    );
  }
  if (purchasesQuery.isError) {
    return (
      <div className="mx-auto max-w-2xl">
        <ErrorState
          message={purchasesQuery.error.message}
          onRetry={() => purchasesQuery.refetch()}
        />
      </div>
    );
  }

  // The transaction the server knows, refined by anything the pay attempt told
  // us: a decline reads as failed, a completion as paid.
  const status: TransactionStatus = declined
    ? "failed"
    : (statusOverride ?? purchase!.transaction.status);
  const paidAt = paidAtOverride ?? purchase!.transaction.paid_at;

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const digits = cardDigits(card); // strip the grouping spaces on submit
    if (!isValidCardNumber(digits)) {
      setCardError("Enter a card number of 12–19 digits.");
      return;
    }
    setCardError(null);
    setDeclined(false);
    pay.mutate(digits);
  }

  return (
    <div className="mx-auto flex w-full max-w-2xl flex-col gap-5">
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-zinc-900 sm:text-3xl">
          Checkout
        </h1>
        <p className="mt-1 text-sm text-zinc-500">
          Settle your winning bid — payment is simulated in this demo.
        </p>
      </div>

      <OrderSummary purchase={purchase!} status={status} />

      {status === "completed" ? (
        <SuccessPanel purchase={purchase!} paidAt={paidAt} />
      ) : status === "expired" ? (
        <ExpiredPanel purchase={purchase!} />
      ) : (
        <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
          <h2 className="font-semibold text-zinc-900">Payment</h2>
          <p
            role="note"
            className="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm font-medium text-amber-800"
          >
            Demo checkout — no real payments
          </p>

          <form onSubmit={onSubmit} className="mt-4 flex flex-col gap-4">
            <label className={labelClass}>
              Card number
              <input
                type="text"
                required
                inputMode="numeric"
                autoComplete="cc-number"
                maxLength={23}
                value={card}
                onChange={(e) => {
                  setCard(formatCardNumber(e.target.value));
                  setCardError(null);
                  setDeclined(false);
                }}
                className={inputClass}
                placeholder="4242 4242 4242 4242"
              />
              <span className="text-xs font-normal text-zinc-500">
                Use {DECLINE_CARD_FORMATTED} to see a failed payment.
              </span>
              {cardError ? (
                <span className={fieldErrorClass}>{cardError}</span>
              ) : null}
            </label>

            {declined ? (
              <p
                role="alert"
                className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700"
              >
                Payment declined — try a different card.
              </p>
            ) : null}

            {pay.isError &&
            !notFound &&
            !(pay.error instanceof ApiError && pay.error.code === "transaction_expired") ? (
              <p
                role="alert"
                className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700"
              >
                {pay.error.message}
              </p>
            ) : null}

            <button
              type="submit"
              disabled={pay.isPending}
              className={primaryButtonClass}
            >
              {pay.isPending
                ? "Paying…"
                : `Pay ${formatUSD(purchase!.transaction.amount_cents)}`}
            </button>
          </form>
        </section>
      )}
    </div>
  );
}
