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

In development, a `.env` file is loaded automatically (via `godotenv`) if
present; its absence is not an error. In production (Railway), env vars are
injected directly by the platform.

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
│   ├── handlers/                 # signup, login, refresh, logout, me
│   ├── middleware/                # RequireAuth, CORS, login rate-limit
│   └── router/                    # Gin route registration
└── railway.json
```
