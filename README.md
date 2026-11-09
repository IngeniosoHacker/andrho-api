# andrho-api

Accounts / authentication backend for the AndRho ecosystem. It owns account
signup, login, and JWT issuance, and registers each new account's `site_id`
with the shared **WebTracker** Postgres database so analytics ingestion
recognizes the site immediately.

`andrho-tracker-dashboard` verifies the access tokens this service issues
(same `JWT_SECRET`, HS256) and uses the `site_id` claim to authorize access to
`/api/sites/:siteId/*`. See [JWT contract](#jwt-contract) below — it must stay
stable for that integration to keep working.

## Stack

- Go 1.27, [Gin](https://github.com/gin-gonic/gin)
- PostgreSQL via [pgx/v5 pgxpool](https://github.com/jackc/pgx) — **two** separate pools:
  - `DATABASE_URL`: this service's own `accounts` / `refresh_tokens` tables.
  - `TRACKER_DATABASE_URL`: WebTracker's database, touched only to upsert into its `sites` table.
- Redis via [go-redis/v9](https://github.com/redis/go-redis), used for login rate-limiting.
- JWT via [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt).
- bcrypt via `golang.org/x/crypto/bcrypt`.

## Running locally

```bash
cp .env.example .env
# fill in DATABASE_URL, TRACKER_DATABASE_URL, REDIS_URL, JWT_SECRET

# Postgres + Redis, e.g. via Docker:
docker run -d --name andrho-api-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=andrho_api -p 5432:5432 postgres:16
docker run -d --name andrho-api-redis -p 6379:6379 redis:7
# and a second database for the tracker's `sites` table (or point
# TRACKER_DATABASE_URL at your real WebTracker Postgres instance):
docker exec -it andrho-api-pg psql -U postgres -c "CREATE DATABASE tracker_test;"
docker exec -i andrho-api-pg psql -U postgres -d tracker_test < ../WebTracker/src/db/schema.sql

go run .
```

The `accounts` schema (`internal/db/schema.sql`) is applied automatically on
boot — `CREATE TABLE IF NOT EXISTS` throughout, safe to run on every start.
This service never writes DDL to the tracker database; it assumes WebTracker
already owns and migrates its own schema.

## Environment variables

| Variable                  | Default | Notes                                                                 |
|----------------------------|---------|------------------------------------------------------------------------|
| `PORT`                     | `8080`  | HTTP port.                                                             |
| `DATABASE_URL`              | —       | Postgres connection string for this service's own tables.             |
| `TRACKER_DATABASE_URL`      | —       | Postgres connection string for WebTracker's database (`sites` table only). |
| `REDIS_URL`                 | —       | Used for login rate-limiting.                                         |
| `JWT_SECRET`                | —       | HS256 signing secret. **Required** — the process refuses to start without it. Must match `andrho-tracker-dashboard`. |
| `JWT_ACCESS_TTL_MINUTES`    | `30`    | Access token lifetime.                                                |
| `JWT_REFRESH_TTL_DAYS`      | `30`    | Refresh token lifetime.                                               |
| `ALLOWED_ORIGINS`           | `*`     | CSV of allowed CORS origins for `/auth/*`. `*` or unset allows every origin (dev default — **restrict this to `andrho-tracker-dashboard`'s real domain in production**). |
| `GITHUB_REPOS`              | the 3 AndRho repos | CSV of `owner/name` repos merged by `GET /progress`. |
| `GITHUB_TOKEN`              | —       | Optional GitHub token for `GET /progress` (lifts the 60/hour anonymous limit). |
| `ANTHROPIC_API_KEY`         | —       | Optional. Enables Claude (`internal/ai`): the signup's description reading and suggestion, the dashboard's insights, and later the AndRho panel. Without it, rule-based fallbacks. |
| `ANTHROPIC_MODEL`           | `claude-sonnet-5-5` | Model for those calls. |
| `RSCRIPT_BIN`, `R_SCRIPTS_DIR` | `Rscript`, `stats` | Only for development: a local R used when `VPS_HOST` is unset. In production the Web section's R analyses run on the VPS (see below). |
| `VPS_HOST`, `VPS_PORT`, `VPS_USER`, `VPS_SSH_KEY`, `VPS_HOST_KEY`, `VPS_SCRIPTS_DIR` | — | The remote VPS where provisioning scripts run over SSH (see `../VPS/README.md`). Without `VPS_HOST`, ERP and WhatsApp provisioning wait for it. |
| `ODOO_ALLOW_PRIVATE`        | — | `true` lets `POST /account/erp` reach private addresses (development only). |
| `ODOO_DB_MODE`              | `shared` | `shared`, `dedicated` or `mixed` (dedicated for Galaxy). |
| `DASHBOARD_URL`             | — | The dashboard's address (e.g. `https://dev.andrho.com/dashboard/`). Each provisioned ERP sends people back there: its login page, logout and "Volver a AndRho". |
| `SECRETS_KEY`               | `JWT_SECRET` | Encrypts stored credentials (Odoo). Set once and keep. |
| `ANDRHO_ADMINS`             | — | CSV of emails: AndRho staff admins (internal metrics). |
| `ANDRHO_CREATORS`           | `test@andrho.dev` | CSV of emails: Creators (build customer sites). |
| `SITE_HOST_PATTERN`         | `{id}.web.andrho.com` | A client site's host; `{id}` is its subdomain. |
| `SITE_WILDCARD`             | `true` | One wildcard (`*.web.andrho.com` on sites-router) covers every site. `false` turns on the per-host Railway domain + Porkbun DNS (only for a client's own domain). |
| `RAILWAY_API_TOKEN`         | — | Railway API token with access to the `Clients` project. |
| `RAILWAY_CLIENTS_PROJECT_ID`, `RAILWAY_CLIENTS_ENVIRONMENT_ID` | — | The `Clients` project and its `production` environment, where each `site-<account_id>` service is created. Without them sites stay `deploying` and `account_sites.domain_state` says what's missing. |
| `SITES_API_URL`             | — | andrho-api's public URL, given to each client's site service (`ANDRHO_API_URL` there). |
| `SITES_TEMPLATE_REPO`, `SITES_TEMPLATE_BRANCH`, `SITES_TEMPLATE_ROOT` | `IngeniosoHacker/andrho-tracker-dashboard`, `main`, `sites` | What each client's service is deployed from. |
| `RAILWAY_ROUTER_SERVICE_ID` | — | sites-router's id, only for `SITE_WILDCARD=false`, a client's own domain or `cmd/sites-wildcard`. Replaces `RAILWAY_SITES_SERVICE_ID` (still read as a fallback). |
| `PORKBUN_API_KEY`, `PORKBUN_SECRET_KEY`, `SITE_DNS_ZONE` | —, —, `andrho.com` | Optional: only for own domains (`SITE_WILDCARD=false`) and `cmd/sites-wildcard`. |
| `PAYMENTS_ENABLED`          | `false` | Checkout charges through a `PaymentProvider` (`internal/billing`). There is no real provider yet: accounts are created with `billing_status=pending_payment`, `payment_skipped=true`. |
| `META_APP_ID`, `META_APP_SECRET` | — | AndRho's Meta app (venta en vivo). Without both, sellers can organize lives and print cards, but can't connect Facebook. |
| `META_VERIFY_TOKEN`         | — | Any long random string; the same one goes in Meta's Webhooks setup for `/meta/webhook`. |
| `API_PUBLIC_URL`            | — | This API's public URL (`https://api.andrho.com`). Facebook Login returns to `<API_PUBLIC_URL>/meta/callback`. |
| `META_GRAPH_VERSION`        | `v27.0` | Graph API version. |
| `META_LOGIN_CONFIG_ID`      | — | The Facebook Login for Business configuration id (founder/meta-cuenta-oficial.md §5). Needed in production: the app uses Login for Business, which WhatsApp Embedded Signup also requires. Without it the dialog asks for the permissions by name (classic Login, development only). |
| `LIVE_CONFIRM_MINUTES`      | `15` | How long a buyer has to tap "Sí" before the item goes to the next person who commented it. |

In development, a `.env` file is loaded automatically (via `godotenv`) if
present; its absence is not an error. In production (Railway), env vars are
injected directly by the platform.

## Deploying on Railway

Deploy this as a **new service in the same Railway project** where `WebTracker`
and `andrho-tracker-dashboard` already run (so it can reference their existing
Postgres/Redis plugins instead of provisioning new ones).

1. **New service** → deploy from the `andrho-api` repo (GitHub) or via `railway up`
   from this directory. `railway.json` already sets the build/start commands
   (`go build -o bin/andrho-api .` / `./bin/andrho-api`), Nixpacks will detect
   Go from `go.mod` automatically.

2. **Postgres — can you reuse the same instance as the tracker? Yes.** Two ways,
   pick based on how much isolation you want:
   - **Simplest (works with zero extra setup):** point `DATABASE_URL` at the
     *exact same* `${{Postgres.DATABASE_URL}}` reference already used by
     `andrho-tracker-dashboard`, and set `TRACKER_DATABASE_URL` to that same
     value too. `accounts`/`refresh_tokens` (this service) and
     `sites`/`sessions`/`pageviews`/... (WebTracker) are just different tables
     in the same database — no name collisions, each service's
     `CREATE TABLE IF NOT EXISTS` migration only touches its own tables. This
     is the cheapest option (one Postgres plugin total) and is fine at this
     stage.
   - **Cleaner (separate logical database, same instance/plugin, a bit more
     setup):** connect once with `psql` to the shared Postgres and run
     `CREATE DATABASE andrho_api;`, then build `DATABASE_URL` by hand from the
     plugin's own variables with the dbname swapped, e.g.
     `postgresql://${{Postgres.PGUSER}}:${{Postgres.PGPASSWORD}}@${{Postgres.PGHOST}}:${{Postgres.PGPORT}}/andrho_api`.
     `TRACKER_DATABASE_URL` stays as `${{Postgres.DATABASE_URL}}` (the
     original tracker database). Keeps the accounts schema fully isolated from
     analytics data — nicer long-term, not required to ship today.
   - Either way: **WebTracker must have deployed (and migrated) at least once
     before the first signup** — signup fails if the `sites` table doesn't
     exist yet in whatever database `TRACKER_DATABASE_URL` points at.

3. **Redis — can you reuse the same instance? Yes, safely.** This service only
   uses Redis for login rate-limiting, under keys prefixed `ratelimit:login:*`
   — that never collides with WebTracker's own keys (`queue:events`,
   `sessions:active`, `session:meta:*`). Just set
   `REDIS_URL=${{Redis.REDIS_URL}}` pointing at the same Redis plugin
   WebTracker uses. No new plugin needed.

4. **Env vars** to set on this service (Settings → Variables):
   ```
   DATABASE_URL=${{Postgres.DATABASE_URL}}          # or the andrho_api-specific URL, see above
   TRACKER_DATABASE_URL=${{Postgres.DATABASE_URL}}  # always the tracker's database
   REDIS_URL=${{Redis.REDIS_URL}}
   JWT_SECRET=<generate one, e.g. `openssl rand -base64 48`>
   ALLOWED_ORIGINS=https://<your andrho-tracker-dashboard public domain>
   ```
   Leave `PORT` unset — Railway injects its own and this service already reads
   `process.env.PORT` (`config` package) / binds to it.

5. **Generate a public domain** for this service (Settings → Networking →
   Generate Domain) — `andrho-tracker-dashboard`'s browser-side code
   (`VITE_ANDRHO_API_URL`) and server-side code (`ANDRHO_API_URL`) both need
   this URL.

6. **Copy the same `JWT_SECRET` value to `andrho-tracker-dashboard`'s service
   variables too.** Both services must sign/verify with the identical secret
   or every request to the dashboard will 401. This is the one variable that
   *must* match exactly across both services — everything else is independent.

## Endpoints

All endpoints are under `/auth`. Responses are JSON; errors are always
`{"error": "<message>"}` with an appropriate HTTP status.

### `POST /auth/signup`

```bash
curl -X POST http://localhost:8080/auth/signup \
  -H "Content-Type: application/json" \
  -d '{"email":"jane@acme.com","password":"supersecret123","company_name":"Acme Rocket Co"}'
```

```json
{
  "access_token": "...",
  "refresh_token": "...",
  "account": { "email": "jane@acme.com", "company_name": "Acme Rocket Co", "site_id": "acme-rocket-co" }
}
```

- `201` on success, `400` on validation failure (basic email format, password
  ≥ 8 chars, non-empty company_name), `409` if the email is already registered.
- `site_id` is derived by slugifying `company_name` (lowercase, non-alphanumeric
  runs collapsed to `-`, trimmed). If that slug is already taken, a random
  4-hex-char suffix is appended and retried (a few attempts) until a free id
  is found — e.g. `acme-rocket-co-7746`.
- The new `site_id` is also upserted into WebTracker's `sites` table
  (`name = company_name`), mirroring the `INSERT ... ON CONFLICT (id) DO
  NOTHING` pattern already used by `WebTracker/src/routes/collect.js`'s
  `siteIsAllowed`, so ingestion recognizes the site immediately.

### `POST /auth/login`

```bash
curl -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"jane@acme.com","password":"supersecret123"}'
```

Same response shape as signup. `200` on success, `401` on bad credentials
(the message is intentionally generic — it never reveals whether the email
exists). `429` if the email+IP pair has exceeded 10 attempts in the last 15
minutes (a fixed-window counter in Redis).

### `POST /auth/refresh`

```bash
curl -X POST http://localhost:8080/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{"refresh_token":"<refresh_token>"}'
```

```json
{ "access_token": "...", "refresh_token": "..." }
```

`200` with a **new** access token **and** a new refresh token — refresh
tokens are rotated on every use (see [Design decisions](#design-decisions)).
`401` if the token is unknown, already used/rotated away, or expired.

### `POST /auth/logout`

```bash
curl -X POST http://localhost:8080/auth/logout \
  -H "Content-Type: application/json" \
  -d '{"refresh_token":"<refresh_token>"}'
```

Deletes the refresh token's row. Always `200 {"ok": true}` — logout is
idempotent, so calling it twice (or with an already-invalid token) is not an
error.

### `GET /auth/me`

```bash
curl http://localhost:8080/auth/me -H "Authorization: Bearer <access_token>"
```

```json
{ "email": "jane@acme.com", "company_name": "Acme Rocket Co", "site_id": "acme-rocket-co" }
```

`200` on a valid, unexpired access token; `401` otherwise. Reads the account
fresh from the database (keyed by the JWT's `sub`) rather than trusting the
token's claims verbatim, so it reflects any changes made after the token was
issued.

### `POST /waitlist`

Public, rate-limited (5/hour per IP). Stores one completed run of andrho's
waiting-list survey (`waitlist.html`). The API matches the answers to a plan
(`internal/recommend`) and stores `recommended_plan` and `suggested_modules`
itself; the response carries them for the survey's last screen:

```json
{ "id": "…", "recommendation": { "plan": "crecer", "scores": {…}, "reasons": ["…"], "suggestedModules": ["analitica", "…"] } }
```

### `POST /waitlist/recommendation`

Public, rate-limited (120/hour per IP), stores nothing. Same body as
`POST /waitlist` (any subset of the answers); returns the `recommendation`
object above. The survey calls it before its plan question to put the
"Recomendado para ti" badge on the matching plan.

Matching (`internal/recommend`): the answers become a few signals (wants
growth, wants order/control, runs on paper, scattered data, company size,
breadth of areas…), each plan has a weight per signal, the highest weighted
sum wins (ties go to the broader plan) and the strongest signals become the
`reasons`. Tuned with the profiles in `recommend_test.go`.

### `GET /progress`

Public. AndRho's development progress for the landing: the repos in
`GITHUB_REPOS` merged into one payload (`totalCommits`, `commitsPerWeek`,
per-repo counts, combined `contributors`, and the 10 latest commits across
all repos in `recent`). Cached in Redis for 10 minutes; if GitHub fails, the
last good copy (kept 7 days) is served. Set `GITHUB_TOKEN` (no scopes needed)
to lift GitHub's 60 requests/hour anonymous limit.

### `POST /auth/register`

The signup with setup (rate-limited, 10/h per IP). Both signup paths, quick
and custom, end here:

```json
{ "email": "...", "password": "...", "company_name": "...", "display_name": "...",
  "setup": "rapida | personalizada",
  "plan": "base | plataforma | despegue | en_orbita | galaxy",
  "addons": [{ "feature_id": "sitio_web_basico", "qty": 1 }],
  "modules": ["inventario", "sitio_web", "whatsapp"],
  "answers": { "...survey answers, optional" }, "description": "optional",
  "phone": "+502 5555 5555",
  "brand": { "essence": "...", "help": false, "logo": "data:image/png;base64,..." },
  "provision": { "meta": "", "whatsapp": "", "web_page": null } }
```

It creates the account (its `site_id` is the **client id** every service is
named after) and the owner, stores the setup (`account_setup`, logo ≤ 1 MB,
PNG/JPG/WebP) and provisions the services (`internal/provision`).

Every account gets an ERP: Odoo with inventory (`stock`) as its base, plus
the Odoo apps its modules need (`analyzer.OdooModules`; the analysis shows
them per package as `odoo_modules`):

| AndRho module | Odoo modules |
| --- | --- |
| *(always)* | `stock` |
| `ventas` | `point_of_sale` |
| `vencimientos` | `product_expiry` |
| `compras` | `purchase` |
| `produccion` | `mrp` |
| `finanzas` | `account` |
| `rrhh` | `hr` |
| `logistica`, `rutas` | `stock_delivery` |
| `agenda`, `citas` | `calendar` |
| `clientes` | `crm` |

The other `provision` fields are optional: whatever is left empty is filled
from what matches it, the WhatsApp module takes `phone`, the website module
sets `web_page`. Each service becomes a job in `provisioning_jobs`, run in
the background:

| Job | What it does | Waits for |
| --- | --- | --- |
| `erp` | Odoo + PostgreSQL on the VPS (`VPS/odoo/provision-client.sh` over SSH, `ODOO_MODULES` = the list above, image built from `andrho_odoo`); the access is stored in `odoo_connections`, password encrypted with `SECRETS_KEY` | `VPS_*` (`esperando_vps`) |
| `whatsapp` | The client in AndRho's WhatsApp solution (`VPS/whatsapp/register-client.sh`) | the VPS, then Meta (`esperando_meta`) |
| `website` | The client's site (its own Railway service, see **Websites**) and a website task for Creators (`creator_tasks`) with the brand brief, assigned by the matching. Only when the plan or the `sitio_web_basico` add-on includes a website; otherwise the site is recorded as `offered` and starts by itself when the plan changes (`PATCH /account/plan`) | Railway (background) |
| `meta` | Nothing yet | Meta's Tech Provider approval |

While the `erp` job runs, the script's `PROGRESS <0-100> <text>` lines
update the job's `progress` and `detail`, so the dashboard shows how far the
ERP is. The ERP's administrator is the account's owner (their email and
name); the script also returns the SSO secret, stored sealed in
`odoo_connections.sso_secret`.

Jobs waiting for the VPS or failed are retried every 10 minutes (5 tries).
The modules can't exceed the plan's limit (1, 2, 3, 6, all). Before
provisioning, the **checkout** (`internal/billing`) prices the plan and its
add-ons from the catalog (`internal/plans`) and stores them in
`account_plan`; with `PAYMENTS_ENABLED=false` nothing is charged
(`billing_status=pending_payment`, `payment_skipped=true`). The response adds
`checkout` (`quote`, `billing_status`, `payment_skipped`) and `site` (when
it was only offered). `GET /admin/billing/skipped` lists the accounts that
skipped payment (`db.PaymentSkippedQuery`, also runnable in psql).

### Plans: `GET /plans`, `POST /plans/quote`

The catalog (`internal/plans`) is the single source of plans, prices,
limits, capabilities and add-ons: base (Q120), plataforma (Q349), despegue
(Q750), en_orbita (Q1,200), galaxy (Q2,990; `galactico` is accepted as its
old id). Gates read capabilities (`plans.Effective(plan, addons)`), never a
plan's name.

`POST /plans/quote` `{ "plan": "plataforma", "addons": [{ "feature_id": "sitio_web_basico" }], "needs": ["sitio_web"] }`
returns `plan` (with `includes`), `next_plan`, `features`
(`getNextPlanFeatures`: what the next plan adds, the analysis' needs first,
each with `featureId`, `label`, `detail`, `addon` {price, max_qty}, `requires`)
and `quote` (`calculateAddonPrice`: `basePrice`, `addons`, `addonsTotal`,
`total`, `nextPlan`, `nextPlanPrice`, `upgradeIsCheaper`). Add-on price =
⌈Δ × weight / Σweights × 1.3⌉ to Q5, so buying everything separately always
costs more than the next plan. Only the next plan's add-ons are offered.

Creator tasks per month come from the plan (0/0/1/2/4) plus `creator_task`
add-ons; accepting a suggestion beyond that answers `402` with
`code: "creator_quota"`. The website build doesn't count.

`GET /account/provisioning` returns the setup, the jobs and the Creators
tasks of the account; `GET /account/logo` the uploaded logo. `/auth/me` now
includes `staff_role` (`admin` | `creator`) for AndRho's own team, synced at
boot from `ANDRHO_ADMINS` / `ANDRHO_CREATORS`.

### The account's ERP: `POST /account/erp` and `GET /erp/*`

The dashboard's Inventario, Clientes, Prospectos, Sucursales and Resumen
read the account's Odoo over JSON-RPC (`internal/odoo`): the one provisioned
at signup, or one the business already had, connected with
`POST /account/erp {url, db, login, password}` (admin+; the login is tested
first, the password stored encrypted, the URL must be public unless
`ODOO_ALLOW_PRIVATE=true` in development). Until there's a connection every
`/erp/*` answers `404 {"error":"erp_not_connected"}`.

| Endpoint | What |
| --- | --- |
| `GET /erp/status` | connected, url, db, login |
| `POST /erp/sso` | `{url, sso}`: where the dashboard's ERP button goes. For the ERP AndRho provisioned, a one-time address (60 s) that opens the user's session there with their AndRho email, no Odoo login (`internal/odoo/sso.go`, checked by `andrho_core` in the andrho_odoo repo). For an Odoo the business connected, its address. |
| `GET /erp/inventory` | products with stock, value at cost, out of stock / low (≤ 5), latest movements |
| `GET /erp/customers` | point-of-sale customers: total, orders, average ticket, last purchase, regulars who stopped coming |
| `GET /erp/leads` | CRM opportunities (or contacts without purchases when there's no CRM) |
| `GET /erp/branches` | warehouses and points of sale: open or not, today's sales |
| `GET /erp/history?category=ventas\|inventario\|clientes` | the operations, newest first |

`POST /erp/import` (editor+) migrates data from the business' previous
system into its Odoo (`internal/etl`): `{kind: products|customers, filename,
dry_run, rows: [{name, code, price, cost, qty, category, barcode} |
{name, email, phone, vat, street, city, company}]}`, up to 5000 rows. Transform
normalizes amounts (`Q1,250.50`, `1.250,50`), validates names, numbers and
emails, and de-duplicates (by code, email or name; the last row wins). A dry
run returns the report and a preview without writing; otherwise Load creates
or updates products (with stock as an inventory adjustment and categories)
and customers. `GET /erp/imports` lists the runs (`etl_runs`).

### Venta en vivo: `/live/*`, `/meta/*` and `GET /erp/locate`

Selling through Facebook lives (`founder/live-sale-spec.md`). Gated by the
plan's `live_sale` capability (Plataforma and up). The ERP side is the
`andrho_live_sale` module (`andrho_odoo` repo): lives and their items,
reservations in stock, shipping cards (PDF, four 10 × 10 cm per letter page)
and buyers (loyalty). AndRho talks to Meta (`internal/meta`) and holds the
rules (`internal/live`):

- `GET /live/status`; `POST /live/setup` (admin) installs the ERP module the
  first time (`update_list` + install).
- `GET /live/meta/connect` (admin) → Facebook Login → `GET /meta/callback`
  stores the seller's Pages (`meta_pages`, token sealed) and subscribes the
  Page to the webhook; `PUT /live/meta/page`, `DELETE /live/meta`.
- `GET /live/products`; `POST /live/sessions` ("Organizar venta": order and
  short live names, created in the ERP); `POST /live/sessions/:id/start`
  (finds the Page's live on air and polls its comments every 2 s; also
  automatic on the `live_videos` webhook); `…/end`; `…/publish` (one photo
  post per unsold item, "comenta Mío").
- A matching comment reserves the item and gets a private reply with Sí/No
  buttons (`live_claims`); "No" or no answer in `LIVE_CONFIRM_MINUTES` passes
  it to the next commenter. "Sí" confirms it in the ERP (card + stock
  reservation) and asks phone and address in Messenger (`live_threads`).
  On posts only "mío"/"Mío" counts; the post is deleted once confirmed.
- `GET /live/cards`, `POST /live/cards/print` (PDF through the ERP entry),
  `GET|PUT /live/settings` (card message and logo, loyalty), `GET /live/metrics`.
- `POST /meta/webhook` checks `X-Hub-Signature-256` with `META_APP_SECRET`;
  `GET /meta/webhook` answers Meta's check with `META_VERIFY_TOKEN`.
- Features by account: live sales are *available* with the plan's
  `live_sale` and *enabled* when the owner turns them on
  (`PUT /account/features/live_sale {enabled}`, `account_features`) or, if
  they never chose, for business type `en_vivo`. `GET /auth/me` carries
  `features`; `/live/*` answer 403 `feature_off` while off; plan, type and
  feature changes show or hide it in the ERP (`andrho_set_enabled`: the
  module's group, implied from internal users).
- The word buyers comment on posts is a setting (`claim_word`, "mío" by
  default), compared without case or accents.
- `GET /erp/locate?q=` is the dashboard's search bar (`internal/locate`):
  synonyms in Spanish ("productos" → Inventario › Productos, "en vivo" → the
  lives); `POST /erp/sso {next}` opens the ERP right there.

End-to-end test (real Postgres and Odoo, fake Graph API):
`LIVE_IT_PG=… LIVE_IT_ODOO=… LIVE_IT_ODOO_DB=… go test -tags integration -run TestLiveEndToEnd ./internal/handlers/`.

### Team presence and preferences

`PATCH /account/me/status {status, note}` (disponible, almorzando,
en_reunion, en_ruta, fuera), `POST /account/me/heartbeat` (the dashboard
calls it every minute while open) and `GET /account/presence` (everyone on
the account, `online` = seen in the last 3 minutes) back the Usuarios
section. `GET|PUT /account/me/preferences` stores a per-person JSON object
(≤ 8 KB), e.g. the cards on Inicio.

### `POST /insights/:section` (the AndRho panel)

Runs when a dashboard section is opened (cached 10 minutes per section;
`?refresh=1` recomputes). `internal/insights` extracts the section's data
(tracker sessions, pageviews and AI crawls for web; products, point-of-sale
orders and CRM leads from Odoo for the ERP sections; inicio/resumen
combine them), processes it with plain statistics (week-over-week change,
z-score anomalies, least-squares trend, days of stock cover, customers who
stopped coming) into findings (`resumen`, `comentario`, `notificacion`), and
proposes suggestions. With `ANTHROPIC_API_KEY`, Claude puts the findings
into plain language (it never computes numbers); otherwise rule-based text.
New proposals are filed as suggestions (`sugerida`) once per title, within
the plan's monthly quota (`quota_reached` tells when one was held back).
`POST /suggestions {section}` asks for one more the same way.

**Type of business** (`internal/business`). On top of the signup's
analysis, each account has a type: `servicios` (plumbing, electricity,
workshops…), `restaurantes` (food) or `ventas` (reselling products it
doesn't make); manufacturing has none for now. It's inferred at signup from
the sectors (or the description) and changed with `GET|PUT
/account/business-type` (admin+). A type adds, after the analysis' own
picks, the modules businesses like it use (`type_modules` in the analyzer's
result), its Odoo apps to the ERP (e.g. `pos_restaurant`, `project`), the
dashboard's section order, what Inicio leads with, and the structure of its
website. Changing it proposes "Prepara tu ERP" (AndRho installs the apps).

**Websites** (`internal/sites`, `internal/provision/sites.go`). A business
without one (`hasWebsite: "no"`, or the `sitio_web` module) whose plan
includes a website gets `<subdomain>.web.andrho.com`: the subdomain is
reserved from its name (lower case, no accents, `[a-z0-9-]`, ≤ 40, no
reserved words, `-2`, `-3`… when taken); its own Railway service
`site-<account_id>` is created in the `Clients` project from the template
(`andrho-tracker-dashboard/sites`, `SITE_MODE=site`), its `*.up.railway.app`
domain removed, and once deployed it's `live` (`domain_state=wildcard`).
sites-router (same code, `SITE_MODE=router`, owns `*.web.andrho.com`)
forwards each host to its service over the private network. Every step is
idempotent and retried each minute (`SiteLoop`); old `web.<id>.andrho.com`
sites are moved at boot, keeping the old host (`legacy_host`) for a 301.
`GET /account/site` and `PUT /account/site/seo` (title, description, image,
Search Console token, branches for LocalBusiness) for the dashboard;
`GET /public/site?host=` for sites-router; `GET /public/sites/:siteId` and
`/logo` for the client's service. One-time wildcard setup:
`go run ./cmd/sites-wildcard` (see its doc comment).

**What the owner sees vs. the evidence.** Every proposal carries three
parts, stored apart on the suggestion: `body` (what to do, plain words),
`explain` (the "¿Por qué?" view: a headline, a chart — `comparacion`,
`tendencia`, `proporcion`, `cambio` or `pasos` —, what it means for the
business and how sure AndRho is, all in plain words) and `evidence` (the
statistics: n, F, p…). `evidence` is never returned by `GET /suggestions`;
only Creators see it, in their task's brief. When Claude rewrites the
proposals it points each one at the computed proposal it's based on, so
the picture and the action are never lost or invented.

**Accepting a suggestion** (`PATCH /suggestions/:id/status` →
`aceptada_en_proceso`, `internal/handlers/accept.go`) runs its `action`:

- `erp_install`: AndRho installs the Odoo apps in the account's ERP itself
  (`odoo.InstallModules`, in the background) and closes the suggestion when
  done. Clientes and Prospectos propose this when the ERP lacks Ventas
  (`point_of_sale`) or CRM (`crm`); these setup suggestions don't count
  against the plan's monthly quota.
- `creator` (the default): a Creator task assigned by `internal/matching`
  (skill 3.0, availability 1.5, load −1.5, on-time record 1.0, knows the
  client 1.0; see the package doc). The owner sees who took it; when the
  Creator delivers, the suggestion closes with the link. Rejecting an
  accepted suggestion cancels its task. Creators set their skills with
  `GET|PUT /creators/skills` (web, social, erp, contenido, diseno). Website
  tasks at signup use the same matching.

CORS allows `GET, POST, PUT, PATCH, DELETE, OPTIONS`: before, `PATCH`/`DELETE`
preflights were rejected and the browser reported "Failed to fetch".

**Web section: statistics in R.** Opening Web also runs
`internal/insights/webstats.go`, which calls two R scripts through
`internal/rstats`. They run **on the VPS**, over the same SSH connection as
the Odoo provisioning (`internal/vps`, `RunInput`): `VPS/stats/run-r.sh <name>`
with the JSON payload on stdin, one process per analysis, 20 s timeout. The
API on Railway needs no R.

- `stats/web_anova.R` — multifactor ANOVA of engagement (time on site and
  pages per visit on log scale, scroll depth) by traffic source, device,
  landing page, weekday/weekend and country, plus the source×device and
  landing×device interactions when their cells are populated. Type II sums
  of squares (right for unbalanced web data), partial η², Tukey HSD between
  levels, Brown-Forsythe and residual-skew checks, and a Kruskal-Wallis
  cross-check. Run twice: all human visits, and Google visits only (which
  landing pages Google sends people to that don't hold them).
- `stats/seo_ranks.R` — per-keyword position trend (least-squares slope,
  p-value) over 60 days of rank checks, and a factorial ANOVA of position on
  on-page factors (keyword in title, in h1, word count band) across the
  site's tracked keywords.

Only effects that are significant (p < 0.05), non-trivial (partial η² ≥ 1%)
and confirmed by the distribution-free test become findings, and each
proposal carries its evidence (n, F, p, ratio) so the Claude translation
can't inflate it.

The scripts are versioned here in `stats/` and installed on the VPS next to
`odoo/` and `whatsapp/` (the VPS needs `r-base-core` and `r-cran-jsonlite`):

```sh
install -d -o andrho -g andrho /opt/andrho/VPS/stats
install -o andrho -g andrho -m 644 stats/*.R stats/run-r.sh /opt/andrho/VPS/stats/
```

Re-run that after changing a script. Without `VPS_HOST` (and no local R) the
Web section keeps its Go statistics plus a plain week-over-week rank
comparison.
`go test ./internal/insights -run WebDeep` runs the whole thing against a
seeded tracker database (see the test for the env vars).

### Staff: `/admin/*` and `/creators/*`

For AndRho's own team (`staff` table, from `ANDRHO_ADMINS` /
`ANDRHO_CREATORS`; admins pass every creator check):

- `GET /admin/overview`: the waiting list in aggregate (counts per answer,
  daily signups, recommended vs. picked plan, latest "una sola cosa"
  answers), the accounts (plans, signup path, modules, provisioning) and
  `urgency`, the ranking of what to build first: requested + ½ suggested
  + ½ pains the module solves + 1½ accounts that switched it on.
- `GET /admin/waitlist?limit=&offset=`: the submissions.
- `GET /admin/game`: the landing minigame, from the tracker's events of
  `ANDRHO_SITE_ID` (`andrho`).
- `GET /admin/tokens?days=30`: AI tokens by source (`development`,
  `whatsapp`, `analyzer`, `insights`), model, day and account.
  `POST /admin/tokens` `{source, entries:[{date, model, input_tokens,
  output_tokens, note}]}` records usage the product doesn't see by itself
  (the team's development usage, by day).
- `POST /admin/accounts/:site_id/erp`: creates (or retries) the ERP of an
  account that has none: accounts from before provisioning existed, like
  AndRho's own (`ANDRHO_SITE_ID`), or whose job gave up after 5 attempts.
  Same job as the signup (progress, credentials and SSO saved); `409` if the
  account already has its ERP or its job is pending, running or done,
  because provisioning a client twice resets its admin password and SSO
  secret. `202` with the job otherwise.
- `GET /creators/tasks`: open tasks plus the creator's own (admins: all).
  `PATCH /creators/tasks/:id` with `action` `aceptar` | `entregar`
  (`delivery_url` required) | `liberar`. `GET /creators/tasks/:id/logo`.

### `POST /analyzer`, `/analyzer/describe` and `/analyzer/suggest`

Public, rate-limited per IP (120/h, 20/h and 30/h). The Analyzer
(`internal/analyzer`) turns what a business tells us into the AndRho setup
it needs.

- `/analyzer/describe` takes `{ "description": "..." }`, the signup's quick
  setup paragraph, and reads it with Claude (`ANTHROPIC_API_KEY`) or,
  without it, keyword rules (`description.source` says which) into
  `description.answers` (the survey's answers, sector questions included),
  `contact` (name, company, email, phone: the boarding pass), `brand`
  (`essence`, `has_brand`), `details` (website, current software, what to
  improve) and a `summary`. The signup then asks only the questions the
  paragraph didn't answer. It also returns the rules' `analysis` of those
  answers.
- `/analyzer/suggest` is the signup's suggestion, made once everything is
  asked: the survey's answers (same body as `/analyzer`, plus
  `websiteUrl`, `currentSoftware`, `improvement`, `satisfactionReason`),
  `description` and `brand: {choice, essence}`. The rules rank the modules
  and pick the plan that fits; Claude ranks them again with the project
  context and explains each one; the two are blended (`analyzer.blend`:
  Borda average for the order; Claude's modules stay, rule-only ones need
  the rules' top half; the areas the business asked for always stay; the
  plan meets halfway). Adds `summary`, `module_reasons` (by key) and
  `source` (`ia` or `reglas`). Without Claude, it's `/analyzer`.
- `/analyzer` takes the survey's answers (same body as
  `/waitlist/recommendation`) and applies only the rules.

All return:

- `needs`: every module the business needs, most important first.
- `packages`: one per plan of the catalog (Base 1 module, Plataforma 2,
  Despegue 3, En órbita 6, Galaxy all), each with its price, label, core
  (`plans.Includes`), the modules it includes, the ones it leaves out
  (`missing`) and a `pitch`.
- `recommended`: always `galaxy`, the package put forward;
  `best_fit` + `reasons`: the plan that strictly fits the answers.

Every Claude call records its tokens in `token_usage` (source `analyzer`).

### Project context for Claude

Every Claude prompt starts with `internal/ai/context/andrho.md` (embedded
in the binary; `ai.WithContext`): what AndRho is, what the site, the signup
and the dashboard do today, who the clients are and how to talk to them.
The module and plan catalog is not in it: prompts that need it get it from
`internal/analyzer/catalog.go`. **When a feature ships or changes, update
that file.**

### Articles: `GET|POST /articles`, `GET|PUT|DELETE /articles/:id`

Auth required; writing needs `editor`+. The account's site articles, managed
from the dashboard's Web > Artículos tab (written there or uploaded as a
`.md` file). Body:

```json
{ "title": "...", "slug": "optional, from the title", "description": "≤320",
  "keywords": ["target searches"], "tags": ["..."], "author": "...",
  "body": "Markdown", "path": "optional, default /articulos/<slug>.html",
  "status": "borrador | publicado" }
```

The slug is unique per account (409 otherwise). Publishing stamps
`published_at` the first time; an empty body can't be published. Lists
leave the body out and return `word_count` for reading time.

### `GET /public/sites/:siteId/articles[/:slug]`

Public. The published articles of the account that owns `site_id` (the
tracker's public id), for the site's own pages: the list without bodies, or
one article with its Markdown body. `Cache-Control: max-age=60`.

## JWT contract

**This is the contract `andrho-tracker-dashboard` relies on — do not change
it without coordinating with that repo.**

- Algorithm: **HS256**, secret from `JWT_SECRET` (shared between both services).
- Access token claims:
  - `sub` — account id (string UUID)
  - `email`
  - `site_id`
  - `company_name`
  - `exp`, `iat` — standard registered claims
- Access token TTL: `JWT_ACCESS_TTL_MINUTES` (default 30).
- The **refresh token is not a JWT**. It's a random 32-byte, base64url-encoded
  opaque string. The client stores and resends it verbatim; the server only
  ever persists its SHA-256 hash (`refresh_tokens.token_hash`), never the
  plaintext.
- Refresh token TTL: `JWT_REFRESH_TTL_DAYS` (default 30).

## Design decisions not fully pinned down by the spec

- **Refresh token rotation: yes.** Every successful `/auth/refresh` deletes
  the presented token's row and issues a brand new access+refresh pair. This
  limits the damage of a leaked refresh token (it can be used at most once
  before either the legitimate client or an attacker invalidates it, making
  reuse detectable) at the cost of clients needing to persist the rotated
  token after every refresh.
- **Error JSON shape:** always `{"error": "<human-readable message>"}`, no
  machine-readable error codes for now — add them later if
  `andrho-tracker-dashboard` needs to branch on specific failures.
- **Login rate limiting:** a simple fixed-window counter in Redis, keyed by
  `client IP + email` from the request body, capped at 10 attempts / 15
  minutes, returning `429`. If Redis is briefly unavailable the middleware
  fails open (login still works) rather than locking everyone out.
- **CORS:** applied only to the `/auth/*` group, allows `GET, POST, OPTIONS`
  and `Content-Type, Authorization` headers. `ALLOWED_ORIGINS=*` (or unset)
  reflects whatever `Origin` header was sent — convenient for local dev, but
  **must** be set to `andrho-tracker-dashboard`'s real origin(s) in production.
- **`site_id` generation** happens application-side (Go), not via a Postgres
  trigger/function, so the same slugify + collision-retry logic is easy to
  unit test and reason about.
- **UUIDs are generated in Go** (`google/uuid`) rather than via Postgres'
  `gen_random_uuid()`, to avoid depending on the `pgcrypto` extension being
  installed/enabled on the accounts database.

## Odoo integration (future work)

The `accounts.odoo_company_id` column exists in the schema but is **not**
populated or used by any endpoint yet. It's reserved for a future phase that
links an AndRho account to its corresponding company record in Odoo.

## Project layout

```
andrho-api/
├── main.go                    # wiring: config, both DB pools, redis, router
├── internal/
│   ├── config/                 # env var loading + defaults
│   ├── db/                     # accounts pool, schema.sql, migrate, accounts + refresh_tokens repos
│   ├── trackerdb/               # second pool -> TRACKER_DATABASE_URL, sites upsert only
│   ├── redisclient/              # redis client setup
│   ├── models/                  # Account / PublicAccount structs
│   ├── auth/                     # bcrypt, JWT issue/verify, refresh token generation, site_id slugify+retry
│   ├── handlers/                 # auth, team, suggestions, waitlist, progress, articles
│   ├── middleware/                # RequireAuth, CORS, login rate-limit
│   └── router/                    # Gin route registration
└── railway.json
```
