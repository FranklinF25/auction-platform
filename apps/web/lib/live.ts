// Pure live-bidding helpers: WebSocket URL math, reconnect backoff, server-clock
// offset, and minimum-bid arithmetic. No React, no DOM, no fetch — every export
// is unit-testable (live.test.ts) and shared by useAuctionSocket.ts and the
// detail-page components. The protocol logic stays here so the hook stays thin.

import type { AuctionStatus } from "@/lib/api";

// --- Pinned WS event contract (docs/PRD.md "Real-time design") ---------------
// Server → client only, JSON text frames, snake_case fields, RFC3339 UTC
// timestamps. The client never sends anything; bids travel over REST.

export interface AuctionStateData {
  auction_id: string;
  status: AuctionStatus;
  current_price_cents: number;
  bid_count: number;
  ends_at: string;
  server_now: string;
  reserve_met: boolean;
  watchers: number;
}

export interface BidPlacedData {
  auction_id: string;
  bid_id: string;
  bidder_name: string;
  amount_cents: number;
  ends_at: string;
  server_now: string;
}

export interface AuctionExtendedData {
  auction_id: string;
  new_ends_at: string;
  server_now: string;
}

export interface PresenceData {
  auction_id: string;
  watchers: number;
}

export type WsMessage =
  | { type: "auction.state"; data: AuctionStateData }
  | { type: "bid.placed"; data: BidPlacedData }
  | { type: "auction.extended"; data: AuctionExtendedData }
  | { type: "presence.update"; data: PresenceData };

/** Parses one text frame; null for malformed frames or unknown event types. */
export function parseWsMessage(raw: string): WsMessage | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const msg = parsed as { type?: unknown; data?: unknown };
  if (
    typeof msg.type !== "string" ||
    typeof msg.data !== "object" ||
    msg.data === null
  ) {
    return null;
  }
  switch (msg.type) {
    case "auction.state":
    case "bid.placed":
    case "auction.extended":
    case "presence.update":
      return msg as WsMessage;
    default:
      return null; // unknown event types are ignored, never fatal
  }
}

/**
 * Builds the socket URL for an auction feed: http(s) origin → ws(s), trailing
 * slashes stripped, auction id path-encoded. The socket connects DIRECTLY to
 * the advertised API origin — the Next.js /api proxy does not forward WebSocket
 * upgrades, which is why the origin comes from GET /api/config.
 */
export function wsUrlFromOrigin(origin: string, auctionId: string): string {
  const base = origin.replace(/\/+$/, "").replace(/^http/, "ws");
  return `${base}/ws/auctions/${encodeURIComponent(auctionId)}`;
}

/** Cap for the reconnect backoff ladder's exponential term. */
export const BACKOFF_CAP_MS = 5000;

/**
 * Reconnect delay: 1000ms * 2^attempt, the exponential term capped at 5000ms,
 * then ±20% jitter so a fleet of dropped clients does not retry in lockstep.
 */
export function backoffDelay(attempt: number): number {
  const base = Math.min(1000 * 2 ** Math.max(attempt, 0), BACKOFF_CAP_MS);
  return base * (0.8 + Math.random() * 0.4);
}

/**
 * Server-clock offset in ms: serverNow − clientNow. Approximate server time
 * with Date.now() + offsetMs. Returns NaN for unparseable input so callers can
 * keep the last good sample.
 */
export function clockOffset(serverNowIso: string, clientNowMs: number): number {
  return Date.parse(serverNowIso) - clientNowMs;
}

/** The smallest valid next bid: current price + min increment (PRD bid rules). */
export function suggestedBidCents(
  currentPriceCents: number,
  minIncrementCents: number,
): number {
  return currentPriceCents + minIncrementCents;
}
