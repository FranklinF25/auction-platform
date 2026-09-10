"use client";

// The bid form: dollars in, cents out (lib/money), REST POST, and the 201
// response applied immediately through applyOwnBid (optimistic but deduped —
// the bid.placed echo for our own bid collapses into the same row). The server
// stays the authority: on 409/403/400 its envelope message is shown verbatim.

import { useMutation } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";
import { suggestedBidCents } from "@/lib/live";
import { formatUSD, parseDollarsToCents } from "@/lib/money";
import type { OwnBid } from "@/lib/useAuctionSocket";
import {
  fieldErrorClass,
  inputClass,
  labelClass,
  primaryButtonClass,
} from "./formStyles";

const SUCCESS_NOTE_MS = 2200;

export default function BidForm({
  auctionId,
  currentPriceCents,
  minIncrementCents,
  me,
  onPlaced,
}: {
  auctionId: string;
  /** Live price — the minimum-next-bid hint tracks it as bids land. */
  currentPriceCents: number;
  minIncrementCents: number;
  me: User;
  onPlaced: (bid: OwnBid) => void;
}) {
  const minCents = suggestedBidCents(currentPriceCents, minIncrementCents);
  const minDollars = (minCents / 100).toFixed(2);

  const [amount, setAmount] = useState(minDollars);
  const [localError, setLocalError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);
  // Last auto-filled suggestion: live price moves update the field only while
  // the bidder hasn't typed their own amount over it.
  const autoFill = useRef(minDollars);
  const noteTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    setAmount((prev) =>
      prev === "" || prev === autoFill.current ? minDollars : prev,
    );
    autoFill.current = minDollars;
  }, [minDollars]);

  useEffect(
    () => () => {
      if (noteTimer.current !== null) clearTimeout(noteTimer.current);
    },
    [],
  );

  const placeBid = useMutation({
    mutationFn: (amountCents: number) =>
      api.placeBid(auctionId, { amount_cents: amountCents }),
    onSuccess: (res) => {
      onPlaced({
        bidId: res.bid_id,
        bidderName: me.name,
        amountCents: res.amount_cents,
        currentPriceCents: res.current_price_cents,
        endsAt: res.ends_at,
        serverNow: res.server_now,
      });
      setLocalError(null);
      setSuccess(true);
      if (noteTimer.current !== null) clearTimeout(noteTimer.current);
      noteTimer.current = setTimeout(() => setSuccess(false), SUCCESS_NOTE_MS);
    },
  });

  const submitError =
    localError ??
    (placeBid.error instanceof ApiError
      ? placeBid.error.message
      : placeBid.error
        ? "Could not place the bid — check your connection and try again."
        : null);

  const onAmountChange = (value: string) => {
    setAmount(value);
    setLocalError(null);
  };

  const onSubmit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (placeBid.isPending) return;
    const cents = parseDollarsToCents(amount);
    if (cents === null) {
      setLocalError("Enter a dollar amount, like 25.00.");
      return;
    }
    // Instant client guard for the obvious case; races still arbitrate
    // server-side and surface the verbatim 409 message below.
    if (cents < minCents) {
      setLocalError(
        `That's below the minimum — bid at least ${formatUSD(minCents)}.`,
      );
      return;
    }
    placeBid.mutate(cents);
  };

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-3" noValidate>
      <label className={labelClass}>
        Your bid (USD)
        <input
          className={inputClass}
          inputMode="decimal"
          autoComplete="off"
          placeholder={minDollars}
          value={amount}
          onChange={(e) => onAmountChange(e.target.value)}
          disabled={placeBid.isPending}
          aria-invalid={submitError != null}
        />
      </label>
      <p className="-mt-1 text-xs text-zinc-500">
        Minimum next bid:{" "}
        <span className="font-semibold tabular-nums text-zinc-700">
          {formatUSD(minCents)}
        </span>
      </p>

      {submitError ? (
        <p role="alert" className={fieldErrorClass}>
          {submitError}
        </p>
      ) : null}
      {success && !submitError ? (
        <p
          role="status"
          className="animate-bid-in text-xs font-medium text-emerald-600"
        >
          Bid placed — you&apos;re the highest bidder.
        </p>
      ) : null}

      <button
        type="submit"
        className={primaryButtonClass}
        disabled={placeBid.isPending}
      >
        {placeBid.isPending ? "Placing bid…" : "Place bid"}
      </button>
    </form>
  );
}
