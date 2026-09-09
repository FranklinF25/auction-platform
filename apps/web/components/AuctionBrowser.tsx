"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { AuctionStatus } from "@/lib/api";
import { api } from "@/lib/api";
import AuctionCard from "./AuctionCard";
import { CardGridSkeleton, EmptyState, ErrorState } from "./States";

const TABS: { value: "" | AuctionStatus; label: string }[] = [
  { value: "", label: "All" },
  { value: "active", label: "Active" },
  { value: "closed", label: "Closed" },
];

export default function AuctionBrowser() {
  const [status, setStatus] = useState<"" | AuctionStatus>("");
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");

  // Debounce the search box so the API is hit once typing settles.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedSearch(search.trim()), 300);
    return () => clearTimeout(id);
  }, [search]);

  const { data, isPending, isError, error, refetch } = useQuery({
    queryKey: ["auctions", status, debouncedSearch],
    queryFn: () =>
      api.listAuctions({
        status: status || undefined,
        q: debouncedSearch || undefined,
        page_size: 24,
      }),
    placeholderData: keepPreviousData,
  });

  const hasFilter = status !== "" || debouncedSearch !== "";

  return (
    <section className="flex flex-col gap-5">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div
          role="tablist"
          aria-label="Filter auctions by status"
          className="flex rounded-lg border border-zinc-200 bg-white p-1 shadow-sm"
        >
          {TABS.map((tab) => (
            <button
              key={tab.label}
              type="button"
              role="tab"
              aria-selected={status === tab.value}
              onClick={() => setStatus(tab.value)}
              className={`rounded-md px-3 py-1.5 text-sm font-medium transition ${
                status === tab.value
                  ? "bg-indigo-600 text-white shadow-sm"
                  : "text-zinc-600 hover:bg-zinc-100"
              }`}
            >
              {tab.label}
            </button>
          ))}
        </div>

        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search auctions by title…"
          aria-label="Search auctions by title"
          className="w-full rounded-lg border border-zinc-200 bg-white px-3 py-2 text-sm shadow-sm outline-none placeholder:text-zinc-400 focus:border-indigo-400 focus:ring-2 focus:ring-indigo-100 sm:w-72"
        />
      </div>

      {isPending ? (
        <CardGridSkeleton count={8} />
      ) : isError ? (
        <ErrorState message={error.message} onRetry={() => refetch()} />
      ) : data.items.length === 0 ? (
        <EmptyState
          title={hasFilter ? "No matching auctions" : "No auctions yet"}
          hint={
            hasFilter
              ? "Try a different search or clear the status filter."
              : "Be the first to put something on the block."
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {data.items.map((auction) => (
            <AuctionCard key={auction.id} auction={auction} />
          ))}
        </div>
      )}

      {data && data.items.length > 0 ? (
        <p className="text-xs text-zinc-500">
          Showing {data.items.length} of {data.total}{" "}
          {data.total === 1 ? "auction" : "auctions"}
        </p>
      ) : null}
    </section>
  );
}
