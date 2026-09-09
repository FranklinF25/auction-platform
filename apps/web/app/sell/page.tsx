"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
  fieldErrorClass,
  inputClass,
  labelClass,
  primaryButtonClass,
} from "@/components/formStyles";
import { CardSkeleton, ErrorState } from "@/components/States";
import { ApiError, api, type CreateAuctionInput } from "@/lib/api";
import { parseDollarsToCents } from "@/lib/money";

const DURATIONS: { value: number; label: string }[] = [
  { value: 5, label: "5 minutes" },
  { value: 30, label: "30 minutes" },
  { value: 60, label: "1 hour" },
  { value: 360, label: "6 hours" },
  { value: 1440, label: "1 day" },
  { value: 4320, label: "3 days" },
  { value: 10080, label: "7 days" },
  { value: 20160, label: "14 days" },
];

interface FormState {
  title: string;
  description: string;
  startingPrice: string;
  minIncrement: string;
  reservePrice: string;
  durationMinutes: string;
}

const initialForm: FormState = {
  title: "",
  description: "",
  startingPrice: "",
  minIncrement: "1.00",
  reservePrice: "",
  durationMinutes: "1440",
};

export default function SellPage() {
  const router = useRouter();

  // Creating an auction requires a session; bounce guests to the login page.
  const { data: me, isPending, isError, error } = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: false,
  });

  useEffect(() => {
    if (isError && error instanceof ApiError && error.status === 401) {
      router.replace("/login");
    }
  }, [isError, error, router]);

  const [form, setForm] = useState<FormState>(initialForm);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [apiError, setApiError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: (input: CreateAuctionInput) => api.createAuction(input),
    onSuccess: (auction) => {
      router.push(`/auctions/${auction.id}`);
    },
    onError: (err) => setApiError(err.message),
  });

  function set<K extends keyof FormState>(key: K, value: string) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  if (isPending) {
    return (
      <div className="mx-auto max-w-2xl">
        <CardSkeleton />
      </div>
    );
  }
  if (isError) {
    // Non-401 failures stay visible instead of silently redirecting.
    return (
      <div className="mx-auto max-w-2xl">
        <ErrorState message={error.message} />
      </div>
    );
  }

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setApiError(null);

    const errors: Record<string, string> = {};
    const starting = parseDollarsToCents(form.startingPrice);
    const increment = parseDollarsToCents(form.minIncrement);
    const reserve =
      form.reservePrice.trim() === ""
        ? null
        : parseDollarsToCents(form.reservePrice);

    if (!form.title.trim()) errors.title = "Title is required.";
    if (starting === null || starting <= 0) {
      errors.startingPrice = "Enter a starting price above $0.";
    }
    if (increment === null || increment <= 0) {
      errors.minIncrement = "Enter a minimum increment above $0.";
    }
    if (reserve !== null && starting !== null && reserve < starting) {
      errors.reservePrice =
        "Reserve must be greater than or equal to the starting price.";
    }
    if (Object.keys(errors).length > 0) {
      setFieldErrors(errors);
      return;
    }
    setFieldErrors({});

    create.mutate({
      title: form.title.trim(),
      description: form.description.trim(),
      starting_price_cents: starting!,
      min_increment_cents: increment!,
      reserve_price_cents: reserve,
      duration_minutes: Number(form.durationMinutes),
    });
  }

  return (
    <div className="mx-auto w-full max-w-2xl">
      <div className="rounded-2xl border border-zinc-200 bg-white p-8 shadow-sm">
        <h1 className="text-2xl font-bold text-zinc-900">Create an auction</h1>
        <p className="mt-1 text-sm text-zinc-500">
          Listing as <span className="font-medium">{me?.name}</span>. Your
          auction goes live immediately.
        </p>

        <form onSubmit={onSubmit} className="mt-6 flex flex-col gap-4">
          {apiError ? (
            <p
              role="alert"
              className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700"
            >
              {apiError}
            </p>
          ) : null}

          <label className={labelClass}>
            Title
            <input
              type="text"
              required
              maxLength={200}
              value={form.title}
              onChange={(e) => set("title", e.target.value)}
              className={inputClass}
              placeholder="Vintage electric guitar"
            />
            {fieldErrors.title ? (
              <span className={fieldErrorClass}>{fieldErrors.title}</span>
            ) : null}
          </label>

          <label className={labelClass}>
            Description
            <textarea
              rows={4}
              maxLength={5000}
              value={form.description}
              onChange={(e) => set("description", e.target.value)}
              className={inputClass}
              placeholder="Condition, provenance, why it's worth bidding on…"
            />
          </label>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <label className={labelClass}>
              Starting price
              <input
                type="text"
                required
                inputMode="decimal"
                value={form.startingPrice}
                onChange={(e) => set("startingPrice", e.target.value)}
                className={inputClass}
                placeholder="50.00"
              />
              {fieldErrors.startingPrice ? (
                <span className={fieldErrorClass}>
                  {fieldErrors.startingPrice}
                </span>
              ) : null}
            </label>

            <label className={labelClass}>
              Min increment
              <input
                type="text"
                inputMode="decimal"
                value={form.minIncrement}
                onChange={(e) => set("minIncrement", e.target.value)}
                className={inputClass}
                placeholder="1.00"
              />
              {fieldErrors.minIncrement ? (
                <span className={fieldErrorClass}>
                  {fieldErrors.minIncrement}
                </span>
              ) : null}
            </label>

            <label className={labelClass}>
              Reserve price (optional)
              <input
                type="text"
                inputMode="decimal"
                value={form.reservePrice}
                onChange={(e) => set("reservePrice", e.target.value)}
                className={inputClass}
                placeholder="Hidden from bidders"
              />
              {fieldErrors.reservePrice ? (
                <span className={fieldErrorClass}>
                  {fieldErrors.reservePrice}
                </span>
              ) : null}
            </label>
          </div>

          <label className={labelClass}>
            Duration
            <select
              value={form.durationMinutes}
              onChange={(e) => set("durationMinutes", e.target.value)}
              className={inputClass}
            >
              {DURATIONS.map((d) => (
                <option key={d.value} value={d.value}>
                  {d.label}
                </option>
              ))}
            </select>
          </label>

          <button
            type="submit"
            disabled={create.isPending}
            className={primaryButtonClass}
          >
            {create.isPending ? "Creating…" : "Put it on the block"}
          </button>
        </form>
      </div>
    </div>
  );
}
