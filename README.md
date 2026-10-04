# Auction Platform

**English** | [Español](README.es.md)

[![CI](https://github.com/FranklinF25/auction-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/FranklinF25/auction-platform/actions/workflows/ci.yml)

A real-time auction platform built as a portfolio piece: Go (chi) API, PostgreSQL 16, Next.js App Router front end, all wired with Docker Compose. Bids placed in one browser appear in every other browser watching the same auction within milliseconds, last-second bids extend the timer (soft close), auctions close on their own, and winners pay through a simulated checkout with a full payment state machine. Run `docker compose up` and the demo landscape below is seeded automatically — two logged-in browser profiles are all you need to evaluate it end to end.

> Screenshot placeholder: animated GIF of two browsers bidding on the same auction goes here.

## 5-minute demo

Requires Docker only. The seed runs on **first boot**, so the live-auction countdowns start when the stack comes up; if you have run it before, reset first.

1. **Start the stack.**

   ```bash
   docker compose down -v   # only if you've run it before: reset DB + demo timeline
   docker compose up        # first run builds; wait for "api listening" and the web container
   ```

   Open <http://localhost:3000>. **Look for:** five seeded auctions — two live bid wars (~4 min and ~15 min left), one zero-bid lot about to close, one sold-and-paid lot, one closed unsold below its reserve.

2. **Log in as two bidders in two browser profiles** (normal window + private window, or two browsers). Profile A: `demo-bidder@auction.dev` / `demo-password`. Profile B: `demo-rival@auction.dev` / `demo-password`.

3. **Open the vinyl auction** ("Miles Davis — Kind of Blue…", ~4 min left, current bid $33.00) in **both** profiles. **Look for:** the "N watching" chip in each window updates when the other profile joins — that's presence over WebSocket, no refresh.

4. **Fight over it.** Take turns outbidding each other (the bid form shows the minimum: current price + increment, e.g. $34.00 next). **Look for:** price, bid history, and countdown update in *both* windows the instant either of you bids — commands go over HTTP, updates arrive as WebSocket pushes.

5. **Trigger the soft close.** Keep bidding into the final 60 seconds. **Look for:** each last-minute bid jumps the timer to 60 seconds from that bid — anti-sniping, so a bid can never land unseen.

6. **Let it close on its own.** Stop bidding and watch either window. The closing worker ticks every second. **Look for:** within ~1s of the timer hitting zero, both windows show the outcome banner — "Sold to …" with the winner's name and final price — pushed live, not on refresh. (Meanwhile the zero-bid espresso lot closes on its own as *unsold* if its ~2 min elapsed.)

7. **Pay as the winner.** In the winning profile, go to **Purchases**: the won lot appears with a pending transaction. Click **Pay**, first with the decline card `4000 0000 0000 0002` — **look for:** "Payment declined — try a different card" (the transaction is failed but retryable). Then pay with any other card number (e.g. `4242 4242 4242 4242`) — **look for:** "Payment complete", and the purchase shows as paid. Unpaid transactions expire 48h after close.

8. **Check the seller's revenue.** Log in as `demo-seller@auction.dev` in a third profile and open the dashboard. **Look for:** sales stats already include revenue from the seeded, already-paid keyboard sale — plus your just-paid vinyl sale — with per-lot status tabs (active / sold / unsold / cancelled).

### Demo accounts & test card

| Account | Role | Password |
|---|---|---|
| `demo-seller@auction.dev` | seller — owns every seeded lot, sees the dashboard | `demo-password` |
| `demo-bidder@auction.dev` | bidder | `demo-password` |
| `demo-rival@auction.dev` | bidder — your sparring partner | `demo-password` |

| Card number | Outcome |
|---|---|
| `4000 0000 0000 0002` | always declined → transaction `failed` (retryable until expiry) |
| any other 12–19 digit number | payment completes |

### Seeded landscape (first boot)

| Lot | State | Purpose |
|---|---|---|
| Miles Davis vinyl | active, 4 bids, $33.00, ends ~4 min | the demo bid war |
| Canon AE-1 camera | active, 3 bids, $105.00, ends ~15 min | second live front |
| Rancilio Silvia espresso | active, 0 bids, ends ~2 min | a zero-bid (unsold) close |
| Mechanical keyboard | closed sold 2h ago, **paid** | seller dashboard revenue on day one |
| Trek bike | closed unsold — bids under the $200 reserve | reserve-not-met close |

## Architecture

```
 Browser
   │  pages + REST commands (login · bid · pay), via the web's /api proxy
   ▼
 Next.js :3000 ──── /api proxy → API_ORIGIN ────▶ Go API :8080 ──── pgx + golang-migrate ────▶ PostgreSQL 16
   ▲                                                  │        (SELECT … FOR UPDATE serializes
   │  WebSocket (read-only event feed)                │         bids, closes and payments)
   └──────────── ws://localhost:8080/ws/auctions/{id} │
                fan-out per-auction room              ▼
        auction.state · bid.placed · auction.extended · auction.closed · presence.update
                              in-memory hub ◀── publish-after-commit
```

The browser only ever talks to the Next.js origin for REST (a runtime middleware proxy forwards `/api` to the Go service — no CORS), but opens the WebSocket **directly** to the API origin it learns from `GET /api/config`; session cookies are host-scoped, so they ride along across ports.

The backend is a pragmatic hexagon. `internal/auction` and `internal/auth` are the domain: pure, synchronous, testable without infrastructure, stdlib-only (one documented exception — `bcrypt.go`, a thin adapter behind the `PasswordHasher` port). The domain *owns its ports* — `Clock`, `Repository`, `EventPublisher`, password hashing — and services sit directly on domain + ports with no extra use-case layer. Everything that touches the world is an adapter: `postgres` (persistence), `hub` (fan-out), `httpapi` (REST + WS driving adapter), `closer` (background workers), plus `seed` for the demo data and `cmd/server` as the composition root.

```
apps/api
├── cmd/server/         composition root: env, wiring, graceful shutdown, seed
├── internal/auction/   domain: Auction, Bid, Transaction, rules, service, ports
├── internal/auth/      domain: users, sessions, ports
├── internal/postgres/  driven: pgx repositories, embedded migrations
├── internal/hub/       driven: in-memory EventPublisher (per-auction rooms)
├── internal/httpapi/   driving: chi REST + the WebSocket endpoint
├── internal/closer/    driving: closing worker (1s) + expiry sweeper (30s)
└── internal/seed/      demo landscape, gated by SEED_DEMO

apps/web                Next.js App Router · TypeScript · TanStack Query · Tailwind
```

## Real-time contract

One WebSocket per browser tab: `GET /ws/auctions/{id}` (guests allowed — the feed is read-only). **Commands go over HTTP, events over the socket**: there are no client→server WS messages at all. Every state-carrying event includes `server_now` so clients can compute their clock offset; money is integer cents; timestamps are RFC3339. A bid committed between join and snapshot may arrive both in the snapshot and as a replayed `bid.placed` — duplicated, never lost; clients converge because every event carries the full authoritative fields.

| Event | Sent when | Key `data` fields |
|---|---|---|
| `auction.state` | on join — full snapshot | `status`, `current_price_cents`, `bid_count`, `ends_at`, `server_now`, `reserve_met`, `watchers` |
| `bid.placed` | a bid transaction commits | `bid_id`, `bidder_name`, `amount_cents`, `ends_at`, `server_now` |
| `auction.extended` | soft close moves `ends_at` | `new_ends_at`, `server_now` |
| `auction.closed` | closing worker closes the lot | `winner_name` (null unless sold), `sold`, `final_price_cents`, `server_now` |
| `presence.update` | a watcher joins/leaves | `watchers` |

## API surface

Base URL `http://localhost:8080` (the web app proxies the same paths under `/api` on :3000). Auth is a session cookie (`auction_session`); errors use a uniform `{error: {code, message}}` envelope. Auction lists accept `q` (title substring), `status` (`active` \| `closed` \| `cancelled`), `page`, `page_size` (max 50).

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /api/auth/register` | — | create account (email, password, name) |
| `POST /api/auth/login` | — | log in, sets the session cookie |
| `POST /api/auth/logout` | — | clear the session |
| `GET /api/me` | ✓ | current user |
| `POST /api/auctions` | ✓ | create auction (title, prices in cents, duration, optional reserve) |
| `GET /api/auctions` | — | list/filter auctions (`q`, `status`, `page`) |
| `GET /api/auctions/{id}` | — | detail, incl. `winner_name` / `you_won` once closed |
| `GET /api/auctions/{id}/bids` | — | bid history, newest first |
| `POST /api/auctions/{id}/bids` | ✓ | place a bid (integer cents) |
| `GET /api/users/me/auctions` | ✓ | seller dashboard — own lots + status |
| `GET /api/users/me/purchases` | ✓ | buyer dashboard — won lots, each with its transaction |
| `GET /api/users/me/sales` | ✓ | seller stats — completed, pending, revenue |
| `POST /api/transactions/{id}/pay` | ✓ | pay a won transaction (card number string) |
| `GET /api/config` | — | client bootstrap (`ws_origin`) |
| `GET /healthz` | — | liveness (used by compose) |

The domain also models a `cancelled` auction status (it's a valid list filter and dashboard tab), but no cancel endpoint is exposed yet.

## Decisions log

| Decision | Why |
|---|---|
| Money in integer cents, never floats | floats can't represent 0.10 exactly; cents make rounding impossible by construction |
| Soft close: last-60s bids extend `ends_at` to bid + 60s | kills sniping — a bid can always be answered |
| Commands over HTTP, events over WS | REST gives free validation/status codes/idempotent semantics; the socket stays a pure fan-out feed with no command parsing to secure |
| In-memory hub | one process, one demo — the honest trade-off; the `EventPublisher` port means Redis pub/sub slots in without touching domain code |
| Winner derived from bids at close (no winner column) | the highest bid *is* the winner; storing it again would be a second source of truth to keep in sync |
| Row-lock concurrency (`SELECT … FOR UPDATE`), not mutexes | correctness survives multiple API instances and matches where the data already lives; in-process mutexes would silently break on scale-out |
| Tx-owned-by-adapter + publish-after-commit | watchers never observe state that could still roll back; the domain stays free of transaction plumbing |
| Simulated payment state machine (`pending → completed/failed/expired`) | the point is the checkout *flow* — retries, expiry, ownership — not card processing |
| Seed goes through the real domain paths (bcrypt, `NewAuction`, `Close`, `CreateTransaction`) | demo data that bypassed the rules could drift from what production writes |

## Milestones

### M1 — foundation

**Shipped:** Go/chi service skeleton; register/login/logout/me with bcrypt + DB-backed sessions; auction create/list/detail; Postgres schema with golang-migrate; Next.js app shell; Docker Compose; GitHub Actions CI (go vet/test, web lint/test/build).

**Verified:** auth + auction unit and `httptest` integration tests green (in-memory fakes); in Docker: register → log in → create an auction → browse it from another profile.

### M2 — live bidding

**Shipped:** the bid hot path serialized by `SELECT … FOR UPDATE`; the in-memory hub; `GET /ws/auctions/{id}` with the five-event contract; live front end (countdown, bid form, history, presence chip) with a direct-to-API socket.

**Verified:** concurrent `PlaceBid` tests (parallel bidders, one winner per increment), WS contract tests, `go test -race` in CI, web units (money, WS parsing, api client); Docker e2e: two browsers, bids and presence sync live.

### M3 — close & dashboards

**Shipped:** closing worker (1s tick, no-overlap, drains in-flight pass on shutdown), winner determination + `pending` transaction creation inside the close transaction; seller dashboard and buyer purchases read models.

**Verified:** closing-worker tests with a fake clock (soft-close race: extended auctions are skipped, not closed); Docker e2e: an auction closes on its own in two open browsers, winner sees the purchase.

### M4 — checkout

**Shipped:** payment endpoint + simulated card state machine (12–19 digits, decline card, 48h expiry); expiry sweeper (30s tick); purchases carry their transaction; checkout UI; seller sales stats; the reviewer demo seed (`SEED_DEMO`).

**Verified:** transaction state-machine, expiry-sweep, and HTTP transaction tests; seed validation tests; Docker e2e: the 5-minute script above — decline, retry, complete, seller revenue.

## Development

Hot-reload stack (air for Go, `next dev` for web, source mounted, same Postgres volume):

```bash
docker compose -f docker-compose.dev.yml up
```

API tests — in-memory fakes, no database needed:

```bash
cd apps/api && go test ./...
```

Race detector in a throwaway Go container (no local toolchain required):

```bash
docker run --rm -v $PWD/apps/api:/app -w /app golang:1.23-alpine \
  sh -c "apk add --no-cache gcc musl-dev && go test -race ./..."
```

Web tests (vitest: money, WS parsing, checkout helpers, api client, dashboard):

```bash
cd apps/web && npm test
```

CI (`.github/workflows/ci.yml`) runs `go vet` + `go test -race` for the API and lint + vitest + `next build` for the web app.

Project layout:

```
.
├── apps/
│   ├── api/                 Go service (chi · pgx · golang-migrate)
│   └── web/                 Next.js App Router (TS · TanStack Query · Tailwind)
├── docker-compose.yml       reviewer stack — built images, seeded demo
├── docker-compose.dev.yml   dev stack — air + next dev, source mounted
└── .github/workflows/ci.yml CI — go vet/test -race, web lint/test/build
```

## Non-goals

Honest scope edges, by design: no real payment processing (simulated cards only), no email or push notifications, single-instance deployment (in-memory hub — Redis pub/sub is the scale-out path), English-only UI.
