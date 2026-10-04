# Auction Platform

[English](README.md) | **Español**

[![CI](https://github.com/FranklinF25/auction-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/FranklinF25/auction-platform/actions/workflows/ci.yml)

Una plataforma de subastas en tiempo real construida como pieza de portafolio: API en Go (chi), PostgreSQL 16 y front end con Next.js App Router, todo orquestado con Docker Compose. Una puja hecha en un navegador aparece en todos los demás que estén mirando la misma subasta en cuestión de milisegundos; las pujas de último segundo extienden el temporizador (soft close), las subastas se cierran solas y los ganadores pagan a través de un checkout simulado con una máquina de estados de pago completa. Ejecuta `docker compose up` y el paisaje demo de abajo se siembra automáticamente — con dos perfiles de navegador con sesión iniciada alcanza para evaluarla de punta a punta.

> Espacio para el screenshot: va acá un GIF animado de dos navegadores pujando por la misma subasta.

## Demo de 5 minutos

Solo requiere Docker. El seed corre en el **primer arranque**, así que las cuentas regresivas de las subastas en vivo empiezan cuando el stack levanta; si ya lo corriste antes, resetea primero.

1. **Levanta el stack.**

   ```bash
   docker compose down -v   # solo si ya lo corriste: resetea la DB + la línea de tiempo demo
   docker compose up        # el primer arranque compila; espera "api listening" y el contenedor web
   ```

   Abre <http://localhost:3000>. **Deberías ver:** cinco subastas sembradas — dos guerras de pujas en vivo (~4 min y ~15 min restantes), un lote sin pujas a punto de cerrar, un lote vendido y pagado, y uno cerrado sin venta por quedar bajo su reserva.

2. **Inicia sesión como dos postores en dos perfiles de navegador** (ventana normal + ventana privada, o dos navegadores). Perfil A: `demo-bidder@auction.dev` / `demo-password`. Perfil B: `demo-rival@auction.dev` / `demo-password`.

3. **Abre la subasta del vinilo** ("Miles Davis — Kind of Blue…", ~4 min restantes, puja actual $33.00) en **ambos** perfiles. **Deberías ver:** el chip "N watching" de cada ventana se actualiza cuando el otro perfil se une — es presencia por WebSocket, sin refresh.

4. **Peleenla.** Alternen pujas superándose mutuamente (el formulario muestra el mínimo: precio actual + incremento, p. ej. $34.00 la siguiente). **Deberías ver:** precio, historial de pujas y cuenta regresiva se actualizan en *ambas* ventanas en el instante en que cualquiera puja — los comandos van por HTTP, las actualizaciones llegan como pushes por WebSocket.

5. **Provoca el soft close.** Sigue pujando dentro de los 60 segundos finales. **Deberías ver:** cada puja de último minuto salta el temporizador a 60 segundos desde esa puja — anti-sniping, para que ninguna puja caiga sin que se pueda responder.

6. **Deja que se cierre sola.** Deja de pujar y mira cualquiera de las dos ventanas. El worker de cierre hace tick cada segundo. **Deberías ver:** en ~1s después de que el temporizador llegue a cero, ambas ventanas muestran el banner de resultado — "Sold to …" con el nombre del ganador y el precio final — pushado en vivo, no al refrescar. (Mientras tanto, el lote de espresso sin pujas se cierra solo como *no vendido* si pasan sus ~2 min.)

7. **Paga como ganador.** En el perfil ganador, ve a **Purchases**: el lote ganado aparece con una transacción pendiente. Haz clic en **Pay**, primero con la tarjeta de rechazo `4000 0000 0000 0002` — **deberías ver:** "Payment declined — try a different card" (la transacción queda fallida pero reintentable). Luego paga con cualquier otro número de tarjeta (p. ej. `4242 4242 4242 4242`) — **deberías ver:** "Payment complete", y la compra queda pagada. Las transacciones impagas expiran 48h después del cierre.

8. **Revisa los ingresos del vendedor.** Inicia sesión como `demo-seller@auction.dev` en un tercer perfil y abre el dashboard. **Deberías ver:** las estadísticas de ventas ya incluyen los ingresos de la venta del teclado sembrada y ya pagada — más tu venta del vinilo recién pagada — con pestañas de estado por lote (active / sold / unsold / cancelled).

### Cuentas demo y tarjeta de prueba

| Cuenta | Rol | Contraseña |
|---|---|---|
| `demo-seller@auction.dev` | vendedor — es dueño de todos los lotes sembrados, ve el dashboard | `demo-password` |
| `demo-bidder@auction.dev` | postor | `demo-password` |
| `demo-rival@auction.dev` | postor — tu sparring | `demo-password` |

| Número de tarjeta | Resultado |
|---|---|
| `4000 0000 0000 0002` | siempre rechazada → transacción `failed` (reintentable hasta expirar) |
| cualquier otro número de 12–19 dígitos | el pago se completa |

### Paisaje sembrado (primer arranque)

| Lote | Estado | Propósito |
|---|---|---|
| Vinilo de Miles Davis | activa, 4 pujas, $33.00, cierra en ~4 min | la guerra de pujas del demo |
| Cámara Canon AE-1 | activa, 3 pujas, $105.00, cierra en ~15 min | segundo frente en vivo |
| Rancilio Silvia espresso | activa, 0 pujas, cierra en ~2 min | un cierre sin pujas (no vendida) |
| Teclado mecánico | cerrada vendida hace 2h, **pagada** | ingresos en el dashboard del vendedor desde el día uno |
| Bici Trek | cerrada sin venta — pujas bajo la reserva de $200 | cierre por reserva no alcanzada |

## Arquitectura

```
 Navegador
   │  páginas + comandos REST (login · bid · pay), vía el proxy /api del web
   ▼
 Next.js :3000 ──── proxy /api → API_ORIGIN ────▶ API Go :8080 ──── pgx + golang-migrate ────▶ PostgreSQL 16
   ▲                                                  │        (SELECT … FOR UPDATE serializa
   │  WebSocket (feed de eventos, solo lectura)       │         pujas, cierres y pagos)
   └──────────── ws://localhost:8080/ws/auctions/{id} │
                fan-out por sala de subasta            ▼
        auction.state · bid.placed · auction.extended · auction.closed · presence.update
                              hub en memoria ◀── publish-after-commit
```

El navegador solo habla con el origen de Next.js para REST (un middleware proxy en runtime reenvía `/api` al servicio Go — sin CORS), pero abre el WebSocket **directamente** al origen de la API que aprende de `GET /api/config`; las cookies de sesión están scopeadas por host, así que viajan entre puertos.

El backend es un hexágono pragmático. `internal/auction` e `internal/auth` son el dominio: puro, síncrono, testeable sin infraestructura, solo stdlib (una excepción documentada — `bcrypt.go`, un adapter fino detrás del puerto `PasswordHasher`). El dominio *es dueño de sus puertos* — `Clock`, `Repository`, `EventPublisher`, el hash de contraseñas — y los servicios se apoyan directo en dominio + puertos sin una capa de use-cases extra. Todo lo que toca el mundo es un adapter: `postgres` (persistencia), `hub` (fan-out), `httpapi` (driving adapter REST + WS), `closer` (workers de fondo), más `seed` para los datos demo y `cmd/server` como raíz de composición.

```
apps/api
├── cmd/server/         raíz de composición: env, wiring, shutdown ordenado, seed
├── internal/auction/   dominio: Auction, Bid, Transaction, reglas, service, puertos
├── internal/auth/      dominio: usuarios, sesiones, puertos
├── internal/postgres/  driven: repositorios pgx, migraciones embebidas
├── internal/hub/       driven: EventPublisher en memoria (salas por subasta)
├── internal/httpapi/   driving: REST con chi + el endpoint WebSocket
├── internal/closer/    driving: worker de cierre (1s) + sweeper de expiración (30s)
└── internal/seed/      paisaje demo, gated por SEED_DEMO

apps/web                Next.js App Router · TypeScript · TanStack Query · Tailwind
```

## Contrato en tiempo real

Un WebSocket por pestaña del navegador: `GET /ws/auctions/{id}` (invitados permitidos — el feed es solo lectura). **Los comandos van por HTTP, los eventos por el socket**: no existen mensajes WS cliente→servidor en absoluto. Todo evento que carga estado incluye `server_now` para que los clientes computen su offset de reloj; el dinero son centavos enteros; los timestamps son RFC3339. Una puja commiteada entre el join y el snapshot puede llegar tanto en el snapshot como replayada como `bid.placed` — duplicada, nunca perdida; los clientes convergen porque cada evento carga los campos autoritativos completos.

| Evento | Se envía cuando | Campos clave de `data` |
|---|---|---|
| `auction.state` | al unirse — snapshot completo | `status`, `current_price_cents`, `bid_count`, `ends_at`, `server_now`, `reserve_met`, `watchers` |
| `bid.placed` | una transacción de puja hace commit | `bid_id`, `bidder_name`, `amount_cents`, `ends_at`, `server_now` |
| `auction.extended` | el soft close mueve `ends_at` | `new_ends_at`, `server_now` |
| `auction.closed` | el worker de cierre cierra el lote | `winner_name` (null salvo venta), `sold`, `final_price_cents`, `server_now` |
| `presence.update` | un watcher entra o sale | `watchers` |

## Superficie de la API

URL base `http://localhost:8080` (el web proxyea los mismos paths bajo `/api` en :3000). La autenticación es una cookie de sesión (`auction_session`); los errores usan un envelope uniforme `{error: {code, message}}`. Los listados de subastas aceptan `q` (subcadena de título), `status` (`active` \| `closed` \| `cancelled`), `page`, `page_size` (máx 50).

| Método y path | Auth | Propósito |
|---|---|---|
| `POST /api/auth/register` | — | crear cuenta (email, contraseña, nombre) |
| `POST /api/auth/login` | — | iniciar sesión, setea la cookie de sesión |
| `POST /api/auth/logout` | — | limpiar la sesión |
| `GET /api/me` | ✓ | usuario actual |
| `POST /api/auctions` | ✓ | crear subasta (título, precios en centavos, duración, reserva opcional) |
| `GET /api/auctions` | — | listar/filtrar subastas (`q`, `status`, `page`) |
| `GET /api/auctions/{id}` | — | detalle, incl. `winner_name` / `you_won` una vez cerrada |
| `GET /api/auctions/{id}/bids` | — | historial de pujas, la más nueva primero |
| `POST /api/auctions/{id}/bids` | ✓ | colocar una puja (centavos enteros) |
| `GET /api/users/me/auctions` | ✓ | dashboard del vendedor — lotes propios + estado |
| `GET /api/users/me/purchases` | ✓ | dashboard del comprador — lotes ganados, cada uno con su transacción |
| `GET /api/users/me/sales` | ✓ | estadísticas del vendedor — completadas, pendientes, ingresos |
| `POST /api/transactions/{id}/pay` | ✓ | pagar una transacción ganada (número de tarjeta como string) |
| `GET /api/config` | — | bootstrap del cliente (`ws_origin`) |
| `GET /healthz` | — | liveness (lo usa compose) |

El dominio también modela un estado `cancelled` para las subastas (es un filtro válido de listado y una pestaña del dashboard), pero todavía no se expone un endpoint de cancelación.

## Registro de decisiones

| Decisión | Por qué |
|---|---|
| Dinero en centavos enteros, nunca floats | los floats no pueden representar 0.10 exactamente; los centavos hacen imposible el redondeo por construcción |
| Soft close: las pujas en los últimos 60s extienden `ends_at` a puja + 60s | mata el sniping — siempre se puede responder una puja |
| Comandos por HTTP, eventos por WS | REST da gratis validación/códigos de estado/semántica idempotente; el socket queda como feed puro de fan-out sin parsing de comandos que asegurar |
| Hub en memoria | un proceso, un demo — el trade-off honesto; el puerto `EventPublisher` permite enchufar Redis pub/sub sin tocar el dominio |
| Ganador derivado de las pujas al cierre (sin columna winner) | la puja más alta *es* el ganador; guardarla de nuevo sería una segunda fuente de verdad que sincronizar |
| Concurrencia por row-lock (`SELECT … FOR UPDATE`), no mutexes | la corrección sobrevive a múltiples instancias de la API y coincide con donde ya viven los datos; los mutexes de proceso romperían silenciosamente al escalar |
| Tx-owned-by-adapter + publish-after-commit | los watchers nunca observan estado que todavía puede rollear back; el dominio queda libre de plumbing transaccional |
| Máquina de estados de pago simulada (`pending → completed/failed/expired`) | el punto es el *flujo* de checkout — reintentos, expiración, ownership — no el procesamiento de tarjetas |
| El seed va por los caminos reales del dominio (bcrypt, `NewAuction`, `Close`, `CreateTransaction`) | los datos demo que bypassearan las reglas podrían divergir de lo que producción escribe |

## Hitos

### M1 — fundación

**Entregado:** esqueleto del servicio Go/chi; register/login/logout/me con bcrypt + sesiones en DB; crear/listar/ver subastas; esquema Postgres con golang-migrate; shell de la app Next.js; Docker Compose; CI con GitHub Actions (go vet/test, web lint/test/build).

**Verificado:** tests de unidad e integración `httptest` de auth + subastas en verde (fakes en memoria); en Docker: registrarse → iniciar sesión → crear una subasta → navegarla desde otro perfil.

### M2 — pujas en vivo

**Entregado:** el hot path de pujas serializado por `SELECT … FOR UPDATE`; el hub en memoria; `GET /ws/auctions/{id}` con el contrato de cinco eventos; front end en vivo (cuenta regresiva, formulario de puja, historial, chip de presencia) con socket directo a la API.

**Verificado:** tests de `PlaceBid` concurrente (postores en paralelo, un ganador por incremento), tests de contrato WS, `go test -race` en CI, unidades web (money, parseo WS, cliente api); e2e en Docker: dos navegadores, pujas y presencia sincronizadas en vivo.

### M3 — cierre y dashboards

**Entregado:** worker de cierre (tick de 1s, sin solapamiento, drena la pasada en curso al apagarse), determinación del ganador + creación de la transacción `pending` dentro de la transacción de cierre; dashboard del vendedor y compras del comprador como read models.

**Verificado:** tests del worker de cierre con fake clock (la carrera del soft close: las subastas extendidas se saltean, no se cierran); e2e en Docker: una subasta se cierra sola en dos navegadores abiertos, el ganador ve la compra.

### M4 — checkout

**Entregado:** endpoint de pago + máquina de estados de tarjeta simulada (12–19 dígitos, tarjeta de rechazo, expiración 48h); sweeper de expiración (tick 30s); las compras cargan su transacción; UI de checkout; estadísticas de venta del vendedor; el seed demo para reviewers (`SEED_DEMO`).

**Verificado:** tests de la máquina de estados de transacciones, del sweeper de expiración y de las transacciones HTTP; tests de validación del seed; e2e en Docker: el script de 5 minutos de arriba — rechazo, reintento, pago completo, ingresos del vendedor.

## Desarrollo

Stack con hot-reload (air para Go, `next dev` para web, código montado, mismo volumen de Postgres):

```bash
docker compose -f docker-compose.dev.yml up
```

Tests de la API — fakes en memoria, sin base de datos:

```bash
cd apps/api && go test ./...
```

Detector de races en un contenedor Go descartable (no requiere toolchain local):

```bash
docker run --rm -v $PWD/apps/api:/app -w /app golang:1.23-alpine \
  sh -c "apk add --no-cache gcc musl-dev && go test -race ./..."
```

Tests web (vitest: money, parseo WS, helpers de checkout, cliente api, dashboard):

```bash
cd apps/web && npm test
```

La CI (`.github/workflows/ci.yml`) corre `go vet` + `go test -race` para la API y lint + vitest + `next build` para el web.

Layout del proyecto:

```
.
├── apps/
│   ├── api/                 servicio Go (chi · pgx · golang-migrate)
│   └── web/                 Next.js App Router (TS · TanStack Query · Tailwind)
├── docker-compose.yml       stack para reviewers — imágenes compiladas, demo sembrado
├── docker-compose.dev.yml   stack dev — air + next dev, código montado
└── .github/workflows/ci.yml CI — go vet/test -race, web lint/test/build
```

## Fuera de alcance

Límites honestos del alcance, por diseño: sin procesamiento de pagos real (solo tarjetas simuladas), sin notificaciones por email ni push, despliegue de una sola instancia (hub en memoria — Redis pub/sub es el camino de escalamiento), UI solo en inglés.
