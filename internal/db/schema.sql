-- Accounts schema for andrho-api.
-- Safe to run multiple times (IF NOT EXISTS everywhere).

CREATE TABLE IF NOT EXISTS accounts (
  id              UUID PRIMARY KEY,
  email           TEXT NOT NULL UNIQUE,
  password_hash   TEXT NOT NULL,
  company_name    TEXT NOT NULL,
  site_id         TEXT NOT NULL UNIQUE,
  odoo_company_id TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Plan gates dashboard features/quotas (e.g. how many suggestions an account
-- can generate per month). Values match the landing page's Pricing tiers
-- (andrho-tracker-dashboard/web/src/components/sections/Pricing.jsx):
-- 'base' | 'despegue' | 'en_orbita' | 'galactico'. No billing/payment
-- processor is wired yet -- PATCH /account/plan only flips this gate, it
-- never charges anything.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS plan TEXT NOT NULL DEFAULT 'base';

CREATE TABLE IF NOT EXISTS refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_account ON refresh_tokens (account_id);

-- Individual logins. An account is now a company/tenant that can have many
-- users; accounts.email/password_hash are legacy (every pre-existing account
-- is backfilled into its own 'owner' user -- see db.BackfillOwnerUsers,
-- called from main.go right after Migrate -- and kept, not dropped, since
-- this schema file only ever grows additively).
CREATE TABLE IF NOT EXISTS users (
  id                UUID PRIMARY KEY,
  account_id        UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  email             TEXT NOT NULL UNIQUE,
  password_hash     TEXT,               -- NULL while an invite is pending
  display_name      TEXT NOT NULL DEFAULT '',
  role              TEXT NOT NULL DEFAULT 'viewer', -- owner | admin | editor | viewer, see internal/auth/roles.go
  invited_by        UUID REFERENCES users(id) ON DELETE SET NULL,
  invite_token_hash TEXT,
  invite_expires_at TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_account ON users (account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_invite_token ON users (invite_token_hash) WHERE invite_token_hash IS NOT NULL;

-- refresh_tokens now belong to a user (whose access token they refresh), not
-- directly to an account. account_id above is kept for rows issued before
-- this column existed; new tokens are issued with both.
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens (user_id);

-- One row per suggestion in the dashboard's AndRho panel (see
-- andrho-tracker-dashboard's public/dashboard/js/insights.js). `section` is
-- the dashboard section it belongs to (web/inventario/clientes/...). They
-- come from the section's analysis (internal/insights): `body` is what to
-- do, `report` the evidence behind it.
CREATE TABLE IF NOT EXISTS suggestions (
  id          UUID PRIMARY KEY,
  account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  section     TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'sugerida', -- sugerida | aceptada_en_proceso | terminada | rechazada
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  report      TEXT NOT NULL DEFAULT '',
  created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_suggestions_account ON suggestions (account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_suggestions_section ON suggestions (account_id, section, created_at DESC);

-- Account activity feed backing the dashboard's "Actualizaciones" tab: one
-- row per meaningful action on the account (suggestion state changes,
-- user/role changes, plan changes, ...). Written by db.LogEvent, called from
-- the handlers that perform those actions.
CREATE TABLE IF NOT EXISTS account_events (
  id            UUID PRIMARY KEY,
  account_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  type          TEXT NOT NULL,
  description   TEXT NOT NULL,
  metadata      JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_events_account ON account_events (account_id, created_at DESC);

-- Pre-launch waiting-list survey (andrho's "MissionForm" -- see that repo's
-- src/components/sections/MissionForm.jsx and PRODUCT.md). Public, unauthenticated:
-- anyone can complete the survey, no account exists yet. Deliberately its own
-- table rather than reusing `accounts` -- these people haven't signed up for
-- anything yet, they're answering a lead-gen questionnaire, and the shape
-- (sectors array, satisfaction rating, free-text answers) doesn't fit the
-- accounts schema at all.
CREATE TABLE IF NOT EXISTS waitlist_submissions (
  id                  UUID PRIMARY KEY,
  name                TEXT NOT NULL,
  company             TEXT NOT NULL,
  email               TEXT NOT NULL,
  sectors             TEXT[] NOT NULL DEFAULT '{}',
  company_size        TEXT NOT NULL DEFAULT '',
  sales_method        TEXT NOT NULL DEFAULT '',
  has_website         TEXT NOT NULL DEFAULT '',
  website_url         TEXT NOT NULL DEFAULT '',
  restaurant_expiry   TEXT NOT NULL DEFAULT '',
  management          TEXT NOT NULL DEFAULT '',
  satisfaction        SMALLINT NOT NULL DEFAULT 0,
  satisfaction_reason TEXT NOT NULL DEFAULT '',
  improvement         TEXT NOT NULL DEFAULT '',
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_waitlist_submissions_created ON waitlist_submissions (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_waitlist_submissions_email ON waitlist_submissions (email);

-- Waiting-list survey v2 (andrho's waitlist.html wizard, see
-- waitinglist.md): one question per screen, sector-specific questions and the
-- modules AndRho suggests from the answers. Added with ADD COLUMN IF NOT
-- EXISTS so existing rows keep working; v1 columns stay as they are.
ALTER TABLE waitlist_submissions
  ADD COLUMN IF NOT EXISTS phone             TEXT   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS sales_methods     TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS sector_answers    JSONB  NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS unsold_handling   TEXT   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS social_networks   TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS messaging_apps    TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS current_software  TEXT   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS pain_points       TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS modules           TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS expectations      TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS plan_interest     TEXT   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS suggested_modules TEXT[] NOT NULL DEFAULT '{}',
  -- The plan internal/recommend matched to the answers; compared with
  -- plan_interest it tells how often people take the recommendation.
  ADD COLUMN IF NOT EXISTS recommended_plan  TEXT   NOT NULL DEFAULT '';

-- Feature-priority queries ("which modules do people ask for most") unnest
-- these arrays; GIN keeps `modules @> '{inventario}'` style filters cheap.
CREATE INDEX IF NOT EXISTS idx_waitlist_submissions_modules ON waitlist_submissions USING GIN (modules);
CREATE INDEX IF NOT EXISTS idx_waitlist_submissions_expectations ON waitlist_submissions USING GIN (expectations);

-- Articles an account publishes on its site (the dashboard's Web > Artículos
-- tab uploads them; the site reads them back through the public
-- /public/sites/:siteId/articles endpoints). `body` is Markdown. `path` is
-- where the article lives on the site, so the tracker's pageviews can be
-- matched to it; empty means the default /articulos/<slug>.html.
CREATE TABLE IF NOT EXISTS articles (
  id              UUID PRIMARY KEY,
  account_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  slug            TEXT NOT NULL,
  title           TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  keywords        TEXT[] NOT NULL DEFAULT '{}', -- the searches the article targets
  tags            TEXT[] NOT NULL DEFAULT '{}',
  author          TEXT NOT NULL DEFAULT '',
  body            TEXT NOT NULL DEFAULT '',
  word_count      INTEGER NOT NULL DEFAULT 0, -- of body, for reading time without loading it
  path            TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'borrador', -- borrador | publicado
  published_at    TIMESTAMPTZ,
  created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_articles_account ON articles (account_id, status, published_at DESC);

-- AI tokens spent, per call (internal/ai). `source` says what for:
-- analyzer (signup analysis), insights (the dashboard's AndRho panel),
-- whatsapp (the WhatsApp integration), development (imported from the
-- team's own AI usage). account_id is empty for calls before an account
-- exists. Read by the staff-only Tokens Usage view.
CREATE TABLE IF NOT EXISTS token_usage (
  id            BIGSERIAL PRIMARY KEY,
  source        TEXT NOT NULL,
  account_id    UUID REFERENCES accounts(id) ON DELETE SET NULL,
  model         TEXT NOT NULL DEFAULT '',
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  note          TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_token_usage_time ON token_usage (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_token_usage_source ON token_usage (source, created_at DESC);

-- AndRho's own team, separate from customer roles: 'admin' sees AndRho's
-- internal metrics (waitlist, minigame, token usage); 'creator' builds
-- customer sites (creator_tasks). Synced at boot from ANDRHO_ADMINS /
-- ANDRHO_CREATORS (emails), see db.SyncStaff.
CREATE TABLE IF NOT EXISTS staff (
  user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL, -- admin | creator
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- What the signup learned about the business (POST /auth/register): the
-- setup path, the survey answers or the description, the modules picked
-- and the brand. One row per account.
CREATE TABLE IF NOT EXISTS account_setup (
  account_id     UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  setup          TEXT NOT NULL DEFAULT 'rapida', -- rapida | personalizada
  answers        JSONB NOT NULL DEFAULT '{}',
  description    TEXT NOT NULL DEFAULT '',
  modules        TEXT[] NOT NULL DEFAULT '{}',
  phone          TEXT NOT NULL DEFAULT '',
  brand_essence  TEXT NOT NULL DEFAULT '',
  brand_help     BOOLEAN NOT NULL DEFAULT false, -- has no brand, wants AndRho to propose one
  logo_type      TEXT NOT NULL DEFAULT '',
  logo           BYTEA,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The minigame discount claimed at signup (the landing's game links to
-- signup.html with the result) and the web tracker's visitor id, which ties
-- the account to the visits that led to it.
ALTER TABLE account_setup
  ADD COLUMN IF NOT EXISTS discount_percent INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS discount_code    TEXT    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS game_session_id  TEXT    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS visitor_id       TEXT    NOT NULL DEFAULT '';

-- One row per thing the signup sets up for an account (internal/provision):
-- kind erp (Odoo + PostgreSQL on the VPS), whatsapp (registration in the
-- WhatsApp solution), website (a Creators task). status: pendiente |
-- en_proceso | listo | esperando_vps | esperando_meta | error.
CREATE TABLE IF NOT EXISTS provisioning_jobs (
  id          UUID PRIMARY KEY,
  account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pendiente',
  detail      TEXT NOT NULL DEFAULT '',
  input       JSONB NOT NULL DEFAULT '{}',
  result      JSONB NOT NULL DEFAULT '{}',
  attempts    INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_provisioning_jobs_account ON provisioning_jobs (account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_provisioning_jobs_status ON provisioning_jobs (status);

-- The account's Odoo, as the VPS script reported it. The password is
-- encrypted with SECRETS_KEY (internal/secret), never stored in clear.
CREATE TABLE IF NOT EXISTS odoo_connections (
  account_id      UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  url             TEXT NOT NULL,
  db_name         TEXT NOT NULL,
  login           TEXT NOT NULL,
  secret          BYTEA NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Work for Creators (designers and developers on AndRho): a customer's
-- website, brand, etc. status: abierta -> aceptada -> entregada.
CREATE TABLE IF NOT EXISTS creator_tasks (
  id           UUID PRIMARY KEY,
  account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL, -- website | brand | ...
  title        TEXT NOT NULL,
  brief        JSONB NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'abierta',
  creator_id   UUID REFERENCES users(id) ON DELETE SET NULL,
  delivery_url TEXT NOT NULL DEFAULT '',
  notes        TEXT NOT NULL DEFAULT '',
  due_at       TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_creator_tasks_status ON creator_tasks (status, created_at);
CREATE INDEX IF NOT EXISTS idx_creator_tasks_creator ON creator_tasks (creator_id, status);

-- Data migrations into the account's ERP (internal/etl): one row per run,
-- dry runs included, with what it did.
CREATE TABLE IF NOT EXISTS etl_runs (
  id          UUID PRIMARY KEY,
  account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
  kind        TEXT NOT NULL,
  dry_run     BOOLEAN NOT NULL,
  filename    TEXT NOT NULL DEFAULT '',
  report      JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_etl_runs_account ON etl_runs (account_id, created_at DESC);

-- The AndRho panel's last analysis per section (internal/insights), so
-- coming back to a section within minutes doesn't recompute it.
CREATE TABLE IF NOT EXISTS insights_runs (
  account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  section     TEXT NOT NULL,
  payload     JSONB NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, section)
);

-- Where each person on the team is (the dashboard's Usuarios): a status
-- they set (disponible, almorzando, en_reunion, en_ruta, fuera) and when
-- the dashboard last saw them (a heartbeat while it's open).
CREATE TABLE IF NOT EXISTS user_presence (
  user_id      UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  status       TEXT NOT NULL DEFAULT 'disponible',
  note         TEXT NOT NULL DEFAULT '',
  status_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-person dashboard preferences, e.g. the cards on Inicio.
CREATE TABLE IF NOT EXISTS user_preferences (
  user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  prefs      JSONB NOT NULL DEFAULT '{}',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
