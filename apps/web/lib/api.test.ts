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
