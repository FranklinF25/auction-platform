"use client";

// The live-auction socket hook. It owns the WebSocket lifecycle only — all
// protocol math (URL building, backoff, clock offset, frame parsing) lives in
// lib/live.ts so it stays unit-testable. The server is authoritative for
// everything (price, ends_at, watchers, status); this hook is a projection with
// a reconnect loop. Commands (bids) never travel over the socket: they go
// through REST and are folded back in via applyOwnBid.

import { useCallback, useEffect, useReducer, useRef } from "react";

import type { AuctionStatus } from "@/lib/api";
import type {
  AuctionClosedData,
  AuctionExtendedData,
  AuctionStateData,
  BidPlacedData,
  PresenceData,
} from "@/lib/live";
import { backoffDelay, clockOffset, parseWsMessage, wsUrlFromOrigin } from "@/lib/live";

export type SocketStatus = "connecting" | "open" | "reconnecting" | "closed";

/**
 * One bid as seen over the wire or from our own POST. The event contract has
 * no created_at, so server_now (the emit instant) stands in for it.
 */
export interface LiveBid {
  id: string;
  bidderName: string;
  amountCents: number;
  createdAt: string;
  own: boolean;
}

/** Post-close outcome: winner display name, sold flag, and the hammer price.
 * Seeded from the SSR detail for already-closed auctions, replaced by the live
 * auction.closed event when the close happens mid-session. */
export interface ClosedOutcome {
  winnerName: string | null;
  sold: boolean;
  finalPriceCents: number;
}

export interface AuctionLiveState {
  /** Socket lifecycle; "closed" is terminal (auction ended or never live). */
  status: SocketStatus;
  auctionStatus: AuctionStatus;
  currentPriceCents: number;
  bidCount: number;
  /** Server-clock end time; render countdowns against offsetMs. */
  endsAt: string;
  reserveMet: boolean;
  watchers: number | null;
  /** Latest server-clock sample: serverNow ≈ Date.now() + offsetMs. */
  offsetMs: number | null;
  /** Session bids, newest first, deduped by bid id (own POSTs included). */
  bids: LiveBid[];
  lastBid: LiveBid | null;
  /** Date.now() of the last auction.extended frame (drives the flash UI). */
  extendedAt: number | null;
  /** Set once the auction closes (seed or live event); null while active. */
  closed: ClosedOutcome | null;
}

/** Server-rendered seed for the projection; the first auction.state replaces
 * the scalar fields and later events keep them moving. */
export interface AuctionLiveInit {
  status: AuctionStatus;
  currentPriceCents: number;
  bidCount: number;
  endsAt: string;
  reserveMet: boolean;
  /** Outcome seed for an already-closed auction (SSR winner view). */
  closed?: ClosedOutcome | null;
}

/** What applyOwnBid needs from the 201 POST response, plus the bidder's name. */
export interface OwnBid {
  bidId: string;
  bidderName: string;
  amountCents: number;
  currentPriceCents: number;
  endsAt: string;
  serverNow: string;
}

export type AuctionLive = AuctionLiveState & {
  applyOwnBid(bid: OwnBid): void;
};

// GET /api/config is fetched once per page load (module-level cached promise),
// never per socket. The HTTP fetch goes through the Next proxy; only the WS
// upgrade connects straight to the advertised origin. Failures are not cached —
// the reconnect loop refetches.
let wsOriginPromise: Promise<string> | null = null;

function fetchWsOrigin(): Promise<string> {
  wsOriginPromise ??= fetch("/api/config", { credentials: "include" })
    .then(async (res) => {
      if (!res.ok) {
        throw new Error(`GET /api/config failed with ${res.status}`);
      }
      const cfg = (await res.json()) as { ws_origin?: string };
      if (!cfg.ws_origin) {
        throw new Error("GET /api/config response is missing ws_origin");
      }
      return cfg.ws_origin;
    })
    .catch((err: unknown) => {
      wsOriginPromise = null;
      throw err;
    });
  return wsOriginPromise;
}

type Action =
  | { kind: "socket"; status: SocketStatus }
  | { kind: "state"; data: AuctionStateData; nowMs: number }
  | { kind: "bid"; data: BidPlacedData; nowMs: number }
  | { kind: "extended"; data: AuctionExtendedData; nowMs: number }
  | { kind: "closed"; data: AuctionClosedData; nowMs: number }
  | { kind: "presence"; data: PresenceData }
  | { kind: "own-bid"; bid: OwnBid; nowMs: number };

/** Keeps the freshest finite offset sample; never trades a good value for NaN. */
function freshestOffset(
  serverNowIso: string,
  nowMs: number,
  prev: number | null,
): number | null {
  const next = clockOffset(serverNowIso, nowMs);
  return Number.isFinite(next) ? next : prev;
}

function prependBid(
  state: AuctionLiveState,
  bid: LiveBid,
  endsAt: string,
  currentPriceCents: number,
  offsetMs: number | null,
): AuctionLiveState {
  if (state.bids.some((b) => b.id === bid.id)) {
    // Duplicate frame (e.g. our own POST echoed back as bid.placed): converge
    // the clock sample, change nothing else.
    return offsetMs === state.offsetMs ? state : { ...state, offsetMs };
  }
  return {
    ...state,
    offsetMs,
    currentPriceCents,
    endsAt,
    bidCount: state.bidCount + 1,
    bids: [bid, ...state.bids],
    lastBid: bid,
  };
}

function reducer(state: AuctionLiveState, action: Action): AuctionLiveState {
  switch (action.kind) {
    case "socket":
      return state.status === action.status
        ? state
        : { ...state, status: action.status };
    case "state": {
      // Full snapshot replace of the scalar fields (join + reconnect sequence).
      // The accumulated bid list survives: the snapshot carries no bids.
      const d = action.data;
      return {
        ...state,
        auctionStatus: d.status,
        currentPriceCents: d.current_price_cents,
        bidCount: d.bid_count,
        endsAt: d.ends_at,
        reserveMet: d.reserve_met,
        watchers: d.watchers,
        offsetMs: freshestOffset(d.server_now, action.nowMs, state.offsetMs),
      };
    }
    case "bid": {
      const d = action.data;
      return prependBid(
        state,
        {
          id: d.bid_id,
          bidderName: d.bidder_name,
          amountCents: d.amount_cents,
          createdAt: d.server_now,
          own: false,
        },
        d.ends_at,
        d.amount_cents, // an accepted bid is by definition the new current price
        freshestOffset(d.server_now, action.nowMs, state.offsetMs),
      );
    }
    case "extended": {
      const d = action.data;
      return {
        ...state,
        endsAt: d.new_ends_at,
        extendedAt: action.nowMs,
        offsetMs: freshestOffset(d.server_now, action.nowMs, state.offsetMs),
      };
    }
    case "closed": {
      // The hammer fell: freeze the projection on the final price and stash
      // the outcome. The server keeps presence flowing, so watchers keep
      // updating through the "presence" action.
      const d = action.data;
      return {
        ...state,
        auctionStatus: "closed",
        currentPriceCents: d.final_price_cents,
        closed: {
          winnerName: d.winner_name,
          sold: d.sold,
          finalPriceCents: d.final_price_cents,
        },
        offsetMs: freshestOffset(d.server_now, action.nowMs, state.offsetMs),
      };
    }
    case "presence":
      return state.watchers === action.data.watchers
        ? state
        : { ...state, watchers: action.data.watchers };
    case "own-bid": {
      const b = action.bid;
      return prependBid(
        state,
        {
          id: b.bidId,
          bidderName: b.bidderName,
          amountCents: b.amountCents,
          createdAt: b.serverNow,
          own: true,
        },
        b.endsAt,
        b.currentPriceCents,
        freshestOffset(b.serverNow, action.nowMs, state.offsetMs),
      );
    }
  }
}

function initAuctionLive(initial: AuctionLiveInit): AuctionLiveState {
  return {
    status: initial.status === "active" ? "connecting" : "closed",
    auctionStatus: initial.status,
    currentPriceCents: initial.currentPriceCents,
    bidCount: initial.bidCount,
    endsAt: initial.endsAt,
    reserveMet: initial.reserveMet,
    watchers: null,
    offsetMs: null,
    bids: [],
    lastBid: null,
    extendedAt: null,
    closed: initial.closed ?? null,
  };
}

// Grace after the projected end before giving up the socket: the server's
// closing ticker runs every second and a soft-close extension reschedules this
// timer, so genuine extensions never drop the feed.
const END_GRACE_MS = 2500;

export function useAuctionSocket(
  auctionId: string,
  initial: AuctionLiveInit,
): AuctionLive {
  const [state, dispatch] = useReducer(reducer, initial, initAuctionLive);
  // Latest-state mirror for event handlers (avoids re-running the socket
  // effect on every projection change).
  const stateRef = useRef(state);
  stateRef.current = state;

  const applyOwnBid = useCallback((bid: OwnBid) => {
    dispatch({ kind: "own-bid", bid, nowMs: Date.now() });
  }, []);

  useEffect(() => {
    // Read-only auctions (closed/cancelled) have no live feed: start terminal.
    if (stateRef.current.auctionStatus !== "active") {
      dispatch({ kind: "socket", status: "closed" });
      return;
    }

    let disposed = false;
    let terminal = false;
    let ws: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let endTimer: ReturnType<typeof setTimeout> | null = null;
    let attempt = 0;

    const clearReconnect = () => {
      if (reconnectTimer !== null) clearTimeout(reconnectTimer);
      reconnectTimer = null;
    };
    const clearEnd = () => {
      if (endTimer !== null) clearTimeout(endTimer);
      endTimer = null;
    };

    const goTerminal = () => {
      if (disposed || terminal) return;
      terminal = true;
      clearReconnect();
      clearEnd();
      dispatch({ kind: "socket", status: "closed" });
      ws?.close(1000, "auction ended");
    };

    // Timer authority is the server clock (PRD): once the projected end passes,
    // with grace for the closer tick, the socket is done. Soft-close
    // extensions reschedule on every ends_at-bearing frame.
    const scheduleEndCheck = (endsAt: string, offsetMs: number | null) => {
      clearEnd();
      const msLeft = Date.parse(endsAt) - (Date.now() + (offsetMs ?? 0));
      if (!Number.isFinite(msLeft)) return;
      endTimer = setTimeout(goTerminal, Math.max(msLeft, 0) + END_GRACE_MS);
    };

    const handleFrame = (raw: unknown) => {
      const msg = parseWsMessage(String(raw));
      if (!msg) return;
      if (msg.data.auction_id !== auctionId) return; // never cross-wire rooms
      const nowMs = Date.now();
      const frameOffset =
        "server_now" in msg.data
          ? clockOffset(msg.data.server_now, nowMs)
          : null;
      const offsetMs = Number.isFinite(frameOffset)
        ? frameOffset
        : stateRef.current.offsetMs;

      switch (msg.type) {
        case "auction.state":
          dispatch({ kind: "state", data: msg.data, nowMs });
          scheduleEndCheck(msg.data.ends_at, offsetMs);
          if (msg.data.status !== "active") goTerminal();
          break;
        case "bid.placed":
          dispatch({ kind: "bid", data: msg.data, nowMs });
          scheduleEndCheck(msg.data.ends_at, offsetMs);
          break;
        case "auction.extended":
          dispatch({ kind: "extended", data: msg.data, nowMs });
          scheduleEndCheck(msg.data.new_ends_at, offsetMs);
          break;
        case "auction.closed":
          // Terminal outcome: stop the projected-end timer and the reconnect
          // ladder, but keep the socket itself open so presence frames keep
          // flowing to whoever stays on the page.
          terminal = true;
          clearReconnect();
          clearEnd();
          dispatch({ kind: "closed", data: msg.data, nowMs });
          break;
        case "presence.update":
          dispatch({ kind: "presence", data: msg.data });
          break;
      }
    };

    const connect = () => {
      if (disposed || terminal) return;
      dispatch({
        kind: "socket",
        status: attempt === 0 ? "connecting" : "reconnecting",
      });
      fetchWsOrigin()
        .then((origin) => {
          if (disposed || terminal) return;
          ws = new WebSocket(wsUrlFromOrigin(origin, auctionId));
          ws.onopen = () => {
            if (disposed) return;
            attempt = 0;
            dispatch({ kind: "socket", status: "open" });
          };
          ws.onmessage = (ev: MessageEvent) => {
            if (disposed) return;
            handleFrame(ev.data);
          };
          ws.onclose = () => {
            if (disposed || terminal) return;
            ws = null;
            dispatch({ kind: "socket", status: "reconnecting" });
            reconnectTimer = setTimeout(connect, backoffDelay(attempt));
            attempt += 1;
          };
          ws.onerror = () => {
            // Normalize transport errors into the onclose reconnect path.
            ws?.close();
          };
        })
        .catch(() => {
          // Config fetch failed (API briefly down): retry with backoff.
          if (disposed || terminal) return;
          dispatch({ kind: "socket", status: "reconnecting" });
          reconnectTimer = setTimeout(connect, backoffDelay(attempt));
          attempt += 1;
        });
    };

    connect();

    return () => {
      disposed = true; // guards every dispatch below against late frames
      clearReconnect();
      clearEnd();
      ws?.close(1000, "unmount");
    };
  }, [auctionId]);

  return { ...state, applyOwnBid };
}
