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

-- One row per suggestion shown under a dashboard section's "Sugerencias"
-- button (see andrho-tracker-dashboard's public/dashboard/app.js). `section`
-- is a free-form string naming which view it belongs to (marketing/ventas/
-- inventarios/kpis/campanas/personal/andrho/...). Generation is currently a
-- stub (see handlers/suggestions.go's generateSuggestionStub) -- body/report
-- are placeholder text until the real data+AI engine exists.
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
