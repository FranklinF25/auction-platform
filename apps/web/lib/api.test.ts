import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, apiOrigin, buildUrl } from "@/lib/api";

describe("buildUrl", () => {
  it("returns the bare path when there are no params", () => {
    expect(buildUrl("/api/auctions")).toBe("/api/auctions");
    expect(buildUrl("/api/auctions", {})).toBe("/api/auctions");
  });

  it("appends numeric params in insertion order", () => {
    expect(buildUrl("/api/auctions", { page: 2, page_size: 10 })).toBe(
      "/api/auctions?page=2&page_size=10",
    );
  });

  it("omits undefined and empty-string params", () => {
    expect(
      buildUrl("/api/auctions", { status: undefined, q: "", page: 1 }),
    ).toBe("/api/auctions?page=1");
  });

  it("encodes search terms", () => {
    expect(buildUrl("/api/auctions", { q: "vintage guitar" })).toBe(
      "/api/auctions?q=vintage+guitar",
    );
    expect(buildUrl("/api/auctions", { q: "50% off" })).toBe(
      "/api/auctions?q=50%25+off",
    );
  });

  it("builds nested resource paths", () => {
    expect(buildUrl("/api/auctions/abc-123/bids", { page_size: 50 })).toBe(
      "/api/auctions/abc-123/bids?page_size=50",
    );
  });
});

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api.placeBid", () => {
  it("POSTs integer cents to the auction's bids endpoint", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(201, {
        bid_id: "b1",
        auction_id: "a1",
        amount_cents: 2600,
        current_price_cents: 2600,
        ends_at: "2026-07-01T00:00:00Z",
        server_now: "2026-06-30T23:59:55Z",
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const res = await api.placeBid("a1", { amount_cents: 2600 });

    expect(res.amount_cents).toBe(2600);
    expect(res.current_price_cents).toBe(2600);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${apiOrigin()}/api/auctions/a1/bids`);
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe('{"amount_cents":2600}');
    expect((init?.headers as Record<string, string>)?.["Content-Type"]).toBe(
      "application/json",
    );
  });

  it("surfaces the API error envelope verbatim (409 bid_too_low)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(409, {
          error: {
            code: "bid_too_low",
            message:
              "bid too low: current price is $25.00 and the minimum increment is $1.00",
          },
        }),
      ),
    );

    const err = await api.placeBid("a1", { amount_cents: 2500 }).then(
      () => null,
      (e) => e,
    );

    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.code).toBe("bid_too_low");
    // The numbers the bidder needs stay verbatim — no client-side rewording.
    expect(err.message).toBe(
      "bid too low: current price is $25.00 and the minimum increment is $1.00",
    );
  });
});

describe("api.listMyAuctions", () => {
  it("GETs the seller-scoped list with params and parses the page envelope", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, {
        items: [
          {
            id: "a1",
            seller_id: "u1",
            title: "Vintage guitar",
            description: "",
            starting_price_cents: 5000,
            min_increment_cents: 100,
            current_price_cents: 7500,
            reserve_met: true,
            bid_count: 3,
            status: "active",
            ends_at: "2026-07-01T00:00:00Z",
            created_at: "2026-06-01T00:00:00Z",
          },
        ],
        page: 1,
        page_size: 50,
        total: 1,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const res = await api.listMyAuctions({
      q: "guitar",
      status: "active",
      page_size: 50,
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(
      `${apiOrigin()}/api/users/me/auctions?q=guitar&status=active&page_size=50`,
    );
    expect(init?.method).toBeUndefined(); // plain GET
    expect(init?.credentials).toBe("include");
    expect(res.page).toBe(1);
    expect(res.total).toBe(1);
    expect(res.items[0].id).toBe("a1");
    expect(res.items[0].current_price_cents).toBe(7500);
  });

  it("hits the bare path when no params are given", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, { items: [], page: 1, page_size: 20, total: 0 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.listMyAuctions();

    expect(fetchMock.mock.calls[0][0]).toBe(
      `${apiOrigin()}/api/users/me/auctions`,
    );
  });
});

describe("api.listMyPurchases", () => {
  it("GETs the buyer-scoped won list and parses the page envelope", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, {
        items: [
          {
            id: "a2",
            seller_id: "u2",
            title: "Mechanical keyboard",
            description: "",
            starting_price_cents: 2000,
            min_increment_cents: 50,
            current_price_cents: 3450,
            reserve_met: true,
            bid_count: 7,
            status: "closed",
            ends_at: "2026-07-02T00:00:00Z",
            created_at: "2026-06-20T00:00:00Z",
            transaction: {
              id: "t1",
              status: "pending",
              amount_cents: 3450,
              expires_at: "2026-07-04T00:00:00Z",
              paid_at: null,
            },
          },
        ],
        page: 1,
        page_size: 50,
        total: 1,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const res = await api.listMyPurchases({ page_size: 50 });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${apiOrigin()}/api/users/me/purchases?page_size=50`);
    expect(init?.method).toBeUndefined(); // plain GET
    expect(init?.credentials).toBe("include");
    expect(res.total).toBe(1);
    expect(res.items[0].title).toBe("Mechanical keyboard");
    expect(res.items[0].status).toBe("closed");
    // M4 join: each item carries its checkout transaction alongside the
    // auction fields.
    expect(res.items[0].transaction.id).toBe("t1");
    expect(res.items[0].transaction.status).toBe("pending");
    expect(res.items[0].transaction.amount_cents).toBe(3450);
    expect(res.items[0].transaction.paid_at).toBeNull();
  });

  it("hits the bare path when no params are given", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, { items: [], page: 1, page_size: 20, total: 0 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.listMyPurchases();

    expect(fetchMock.mock.calls[0][0]).toBe(
      `${apiOrigin()}/api/users/me/purchases`,
    );
  });
});

describe("api.payTransaction", () => {
  it("POSTs the card number to the transaction's pay endpoint", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, {
        transaction_id: "t1",
        status: "completed",
        paid_at: "2026-07-03T12:00:00Z",
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const res = await api.payTransaction("t1", "4242424242424242");

    expect(res.status).toBe("completed");
    expect(res.paid_at).toBe("2026-07-03T12:00:00Z");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${apiOrigin()}/api/transactions/t1/pay`);
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe('{"card_number":"4242424242424242"}');
    expect((init?.headers as Record<string, string>)?.["Content-Type"]).toBe(
      "application/json",
    );
    expect(init?.credentials).toBe("include");
  });

  it("parses a declined card as a 200 outcome, not an error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(200, {
          transaction_id: "t1",
          status: "failed",
          paid_at: null,
        }),
      ),
    );

    const res = await api.payTransaction("t1", "4000000000000002");

    expect(res.status).toBe("failed");
    expect(res.paid_at).toBeNull();
  });

  it("surfaces the 409 transaction_expired envelope verbatim", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(409, {
          error: {
            code: "transaction_expired",
            message: "the payment window for this transaction has closed",
          },
        }),
      ),
    );

    const err = await api.payTransaction("t1", "4242424242424242").then(
      () => null,
      (e) => e,
    );

    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.code).toBe("transaction_expired");
    expect(err.message).toBe(
      "the payment window for this transaction has closed",
    );
  });
});

describe("api.mySales", () => {
  it("GETs the seller's sales summary", async () => {
    const fetchMock = vi.fn<
      (input: string, init?: RequestInit) => Promise<Response>
    >(async () =>
      jsonResponse(200, {
        completed_sales: 3,
        pending_sales: 2,
        revenue_cents: 125000,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const res = await api.mySales();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${apiOrigin()}/api/users/me/sales`);
    expect(init?.method).toBeUndefined(); // plain GET
    expect(init?.credentials).toBe("include");
    expect(res.completed_sales).toBe(3);
    expect(res.pending_sales).toBe(2);
    expect(res.revenue_cents).toBe(125000);
  });
});
