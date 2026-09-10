import { describe, expect, it } from "vitest";
import {
  BACKOFF_CAP_MS,
  backoffDelay,
  clockOffset,
  parseWsMessage,
  suggestedBidCents,
  wsUrlFromOrigin,
} from "@/lib/live";

describe("wsUrlFromOrigin", () => {
  it("maps http to ws and https to wss", () => {
    expect(wsUrlFromOrigin("http://localhost:8080", "a1")).toBe(
      "ws://localhost:8080/ws/auctions/a1",
    );
    expect(wsUrlFromOrigin("https://api.example.com", "a1")).toBe(
      "wss://api.example.com/ws/auctions/a1",
    );
  });

  it("strips trailing slashes before appending the feed path", () => {
    expect(wsUrlFromOrigin("http://localhost:8080/", "a1")).toBe(
      "ws://localhost:8080/ws/auctions/a1",
    );
    expect(wsUrlFromOrigin("https://api.example.com//", "a1")).toBe(
      "wss://api.example.com/ws/auctions/a1",
    );
  });

  it("path-encodes the auction id", () => {
    expect(wsUrlFromOrigin("http://localhost:8080", "not/a uuid")).toBe(
      "ws://localhost:8080/ws/auctions/not%2Fa%20uuid",
    );
  });
});

describe("backoffDelay", () => {
  const SAMPLES = 300;
  // Random multipliers can land an epsilon outside the closed interval due to
  // floating-point rounding; the slack absorbs that without weakening bounds.
  const EPSILON = 1e-6;

  it("doubles per attempt with ±20% jitter", () => {
    for (const attempt of [0, 1, 2]) {
      const base = 1000 * 2 ** attempt;
      for (let i = 0; i < SAMPLES; i++) {
        const delay = backoffDelay(attempt);
        expect(delay).toBeGreaterThanOrEqual(base * 0.8 - EPSILON);
        expect(delay).toBeLessThanOrEqual(base * 1.2 + EPSILON);
      }
    }
  });

  it("caps the exponential term at 5000ms (jitter may exceed it by 20%)", () => {
    let sawAboveCap = false;
    for (let i = 0; i < SAMPLES; i++) {
      const delay = backoffDelay(8);
      expect(delay).toBeGreaterThanOrEqual(BACKOFF_CAP_MS * 0.8 - EPSILON);
      expect(delay).toBeLessThanOrEqual(BACKOFF_CAP_MS * 1.2 + EPSILON);
      sawAboveCap ||= delay > BACKOFF_CAP_MS;
    }
    // Proves the cap applies to the pre-jitter base, not the final delay.
    expect(sawAboveCap).toBe(true);
  });

  it("treats negative attempts as zero", () => {
    for (let i = 0; i < SAMPLES; i++) {
      const delay = backoffDelay(-3);
      expect(delay).toBeGreaterThanOrEqual(800 - EPSILON);
      expect(delay).toBeLessThanOrEqual(1200 + EPSILON);
    }
  });
});

describe("clockOffset", () => {
  it("is positive when the server clock is ahead of the client", () => {
    const serverNow = "2026-07-01T00:00:05.000Z";
    const clientNow = Date.parse("2026-07-01T00:00:00.000Z");
    expect(clockOffset(serverNow, clientNow)).toBe(5000);
  });

  it("is negative when the server clock is behind the client", () => {
    const serverNow = "2026-07-01T00:00:00.000Z";
    const clientNow = Date.parse("2026-07-01T00:00:02.500Z");
    expect(clockOffset(serverNow, clientNow)).toBe(-2500);
  });

  it("is zero when both clocks read the same instant", () => {
    const instant = "2026-07-01T12:00:00.000Z";
    expect(clockOffset(instant, Date.parse(instant))).toBe(0);
  });

  it("returns NaN for unparseable input so callers keep the last sample", () => {
    expect(clockOffset("not a timestamp", 0)).toBeNaN();
  });
});

describe("suggestedBidCents", () => {
  it("adds the increment to the current price", () => {
    expect(suggestedBidCents(2500, 100)).toBe(2600);
    expect(suggestedBidCents(0, 1)).toBe(1);
  });

  it("still works when the increment is zero", () => {
    expect(suggestedBidCents(12345, 0)).toBe(12345);
  });
});

describe("parseWsMessage", () => {
  it("parses each pinned event type", () => {
    const stateFrame =
      '{"type":"auction.state","data":{"auction_id":"a1","status":"active",' +
      '"current_price_cents":2500,"bid_count":3,"ends_at":"2026-07-01T00:00:00Z",' +
      '"server_now":"2026-06-30T23:00:00Z","reserve_met":true,"watchers":2}}';
    expect(parseWsMessage(stateFrame)).toEqual({
      type: "auction.state",
      data: {
        auction_id: "a1",
        status: "active",
        current_price_cents: 2500,
        bid_count: 3,
        ends_at: "2026-07-01T00:00:00Z",
        server_now: "2026-06-30T23:00:00Z",
        reserve_met: true,
        watchers: 2,
      },
    });

    const bidFrame =
      '{"type":"bid.placed","data":{"auction_id":"a1","bid_id":"b9",' +
      '"bidder_name":"Ada","amount_cents":2600,"ends_at":"2026-07-01T00:00:00Z",' +
      '"server_now":"2026-06-30T23:30:00Z"}}';
    const bid = parseWsMessage(bidFrame);
    expect(bid?.type).toBe("bid.placed");
    if (bid?.type === "bid.placed") {
      expect(bid.data.bidder_name).toBe("Ada");
    }

    const presenceFrame =
      '{"type":"presence.update","data":{"auction_id":"a1","watchers":7}}';
    const presence = parseWsMessage(presenceFrame);
    expect(presence?.type).toBe("presence.update");
    if (presence?.type === "presence.update") {
      expect(presence.data.watchers).toBe(7);
    }
  });

  it("returns null for unknown event types (forward compatibility)", () => {
    const frame = '{"type":"auction.closed","data":{"auction_id":"a1"}}';
    expect(parseWsMessage(frame)).toBeNull();
  });

  it("returns null for malformed frames", () => {
    expect(parseWsMessage("not json")).toBeNull();
    expect(parseWsMessage("42")).toBeNull();
    expect(parseWsMessage('{"type":"bid.placed"}')).toBeNull(); // no data
    expect(parseWsMessage('{"data":{"watchers":1}}')).toBeNull(); // no type
  });
});
