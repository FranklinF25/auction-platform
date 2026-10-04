// Typed API client mirroring the Go backend's DTOs exactly (field names are
// snake_case, matching the JSON tags in apps/api/internal/httpapi).
//
// The browser talks only to the Next.js origin: relative /api/... requests are
// proxied to the Go API by middleware.ts, so session cookies stay same-origin
// and the API needs no CORS. Server components bypass the proxy (a server-side
// fetch has no origin to proxy from) and hit API_ORIGIN directly.

export type AuctionStatus = "active" | "closed" | "cancelled";

export interface User {
  id: string;
  email: string;
  name: string;
}

export interface Auction {
  id: string;
  seller_id: string;
  title: string;
  description: string;
  starting_price_cents: number;
  min_increment_cents: number;
  current_price_cents: number;
  reserve_met: boolean;
  bid_count: number;
  status: AuctionStatus;
  ends_at: string;
  /** Actual close timestamp once the worker closed the auction; null otherwise. Present on list and detail representations. */
  closed_at: string | null;
  created_at: string;
  // Detail-only fields (GET /api/auctions/{id}): the paged list endpoints
  // omit them, so only the detail flow reads them.
  /** Winner display name iff closed and sold; null otherwise. */
  winner_name: string | null;
  /** True iff the requester is authenticated and is the winner. */
  you_won: boolean;
}

// -- M4 checkout types ------------------------------------------------------

/** Checkout lifecycle of a won auction's transaction (disjoint from
 * AuctionStatus despite the string-union shape). */
export type TransactionStatus = "pending" | "completed" | "failed" | "expired";

/** The transaction view embedded in each purchases item: what is owed, the
 * payment state, and the remaining payment window. */
export interface Transaction {
  id: string;
  status: TransactionStatus;
  amount_cents: number;
  expires_at: string;
  /** Set once the payment completed; null otherwise. */
  paid_at: string | null;
}

/** A purchases-list item: the auction representation plus its checkout
 * transaction (additive M4 join — every prior Auction field keeps its shape). */
export type Purchase = Auction & { transaction: Transaction };

/** GET /api/users/me/sales: the seller's checkout aggregates. */
export interface SalesSummary {
  completed_sales: number;
  pending_sales: number;
  revenue_cents: number;
}

/** Pinned 200 body of POST /api/transactions/{id}/pay. A declined card is a
 * 200 with status "failed" and paid_at null; "completed" carries paid_at. */
export interface PayTransactionResponse {
  transaction_id: string;
  status: "completed" | "failed";
  paid_at: string | null;
}

export interface Bid {
  id: string;
  bidder_name: string;
  amount_cents: number;
  created_at: string;
}

export interface Page<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
}

// Object type (not interface): TS grants it an implicit index signature, so
// it stays assignable to buildUrl's Record parameter.
export type ListAuctionsParams = {
  status?: AuctionStatus;
  q?: string;
  page?: number;
  page_size?: number;
};

export type ListBidsParams = {
  page?: number;
  page_size?: number;
};

export type ListMyAuctionsParams = {
  q?: string;
  status?: AuctionStatus;
  page?: number;
  page_size?: number;
};

export type ListMyPurchasesParams = {
  page?: number;
  page_size?: number;
};

export interface CreateAuctionInput {
  title: string;
  description: string;
  starting_price_cents: number;
  min_increment_cents: number;
  reserve_price_cents?: number | null;
  duration_minutes: number;
}

export interface PlaceBidInput {
  amount_cents: number;
}

/** Pinned 201 body for an accepted bid; server_now shares the WS events' emit
 * instant so the client can recompute its clock offset. */
export interface PlaceBidResponse {
  bid_id: string;
  auction_id: string;
  amount_cents: number;
  current_price_cents: number;
  ends_at: string;
  server_now: string;
}

/** Error raised for non-2xx responses, carrying the API's error envelope. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Absolute API base for server-side calls; empty (same-origin) in the browser. */
export function apiOrigin(): string {
  return typeof window === "undefined"
    ? process.env.API_ORIGIN || "http://localhost:8080"
    : "";
}

/**
 * Builds an /api URL with the given query params. Undefined and empty-string
 * values are omitted, numbers are stringified, and keys/values are encoded
 * via URLSearchParams.
 */
export function buildUrl(
  path: string,
  params: Record<string, string | number | undefined> = {},
): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") {
      search.append(key, String(value));
    }
  }
  const qs = search.toString();
  return qs ? `${path}?${qs}` : path;
}

interface ErrorEnvelope {
  error?: { code?: string; message?: string };
}

async function toApiError(res: Response): Promise<ApiError> {
  try {
    const body = (await res.json()) as ErrorEnvelope;
    if (body.error?.message) {
      return new ApiError(
        res.status,
        body.error.code ?? "error",
        body.error.message,
      );
    }
  } catch {
    // Non-JSON body; fall through to the generic message.
  }
  return new ApiError(
    res.status,
    "error",
    `Request failed with status ${res.status}`,
  );
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${apiOrigin()}${path}`, {
    credentials: "include",
    cache: "no-store",
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });

  if (!res.ok) {
    throw await toApiError(res);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

export const api = {
  register: (input: { name: string; email: string; password: string }) =>
    request<User>("/api/auth/register", {
      method: "POST",
      body: JSON.stringify(input),
    }),

  login: (input: { email: string; password: string }) =>
    request<User>("/api/auth/login", {
      method: "POST",
      body: JSON.stringify(input),
    }),

  logout: () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),

  me: () => request<User>("/api/me"),

  listAuctions: (params: ListAuctionsParams = {}) =>
    request<Page<Auction>>(buildUrl("/api/auctions", params)),

  getAuction: (id: string) => request<Auction>(`/api/auctions/${id}`),

  listBids: (id: string, params: ListBidsParams = {}) =>
    request<Page<Bid>>(buildUrl(`/api/auctions/${id}/bids`, params)),

  /** Seller dashboard: the signed-in user's own auctions (M3). */
  listMyAuctions: (params: ListMyAuctionsParams = {}) =>
    request<Page<Auction>>(buildUrl("/api/users/me/auctions", params)),

  /** Buyer dashboard: auctions the signed-in user won, newest close first,
   * each joined with its checkout transaction (M4). */
  listMyPurchases: (params: ListMyPurchasesParams = {}) =>
    request<Page<Purchase>>(buildUrl("/api/users/me/purchases", params)),

  /** Seller revenue aggregates for the dashboard strip (M4). */
  mySales: () => request<SalesSummary>("/api/users/me/sales"),

  /** Simulated payment for a transaction the user won (M4). A decline is a
   * 200 with status "failed", not an error — see PayTransactionResponse. */
  payTransaction: (transactionId: string, cardNumber: string) =>
    request<PayTransactionResponse>(`/api/transactions/${transactionId}/pay`, {
      method: "POST",
      body: JSON.stringify({ card_number: cardNumber }),
    }),


  /** 201 response of POST /api/auctions/{id}/bids (pinned M2 contract). */
  placeBid: (id: string, input: PlaceBidInput) =>
    request<PlaceBidResponse>(`/api/auctions/${id}/bids`, {
      method: "POST",
      body: JSON.stringify(input),
    }),

  createAuction: (input: CreateAuctionInput) =>
    request<Auction>("/api/auctions", {
      method: "POST",
      body: JSON.stringify(input),
    }),
};
