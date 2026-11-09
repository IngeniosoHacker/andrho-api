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

-- How far a running job is (0-100), from the VPS script's PROGRESS lines.
ALTER TABLE provisioning_jobs ADD COLUMN IF NOT EXISTS progress INTEGER NOT NULL DEFAULT 0;

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

-- The secret the ERP's andrho_core checks the dashboard's entry tokens
-- with (POST /erp/sso), sealed like the password. NULL for an Odoo the
-- business connected itself: there the button just opens it.
ALTER TABLE odoo_connections ADD COLUMN IF NOT EXISTS sso_secret BYTEA;

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

-- The long signup's kit (internal/brand.Kit): logo, colors and data
-- (tengo | crear | despues; colors also logo, data also cero), the palette
-- the person kept, the head count and how many need access.
ALTER TABLE account_setup ADD COLUMN IF NOT EXISTS kit JSONB NOT NULL DEFAULT '{}';

-- What a business uploads at signup or later from the panel: its logo,
-- images of its brand (their colors go to the palette) and its data from
-- the previous system (internal/intake maps it to Odoo; what maps well is
-- loaded when the ERP is ready). Creators see them in their tasks.
CREATE TABLE IF NOT EXISTS account_assets (
  id           UUID PRIMARY KEY,
  account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL, -- logo | brand_image | data
  filename     TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  size         INTEGER NOT NULL DEFAULT 0,
  data         BYTEA NOT NULL,
  -- images: listo; data: analizado | cargando | cargado | revisar | error
  status       TEXT NOT NULL DEFAULT 'listo',
  meta         JSONB NOT NULL DEFAULT '{}', -- palette, or format + mappings + reports
  uploaded_by  UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_assets_account ON account_assets (account_id, kind, created_at);

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

-- Suggestions carry what the business owner sees apart from the technical
-- backing: `explain` is the "¿Por qué?" view (insights.Explain), `evidence`
-- the statistics behind it (staff and the Creator doing the work only),
-- `action` what accepting does (insights.Action). `report` stays the
-- outcome told back to the owner when it's done.
ALTER TABLE suggestions ADD COLUMN IF NOT EXISTS evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE suggestions ADD COLUMN IF NOT EXISTS explain JSONB;
ALTER TABLE suggestions ADD COLUMN IF NOT EXISTS action JSONB;
-- Before `evidence` existed, the pipeline stored it in `report`: move it
-- once, for suggestions that never got an outcome.
UPDATE suggestions SET evidence = report, report = ''
 WHERE evidence = '' AND report <> '' AND status IN ('sugerida', 'aceptada_en_proceso', 'rechazada');

-- What each Creator does best, for internal/matching: web, social, erp,
-- contenido, diseno (empty = generalist).
ALTER TABLE staff ADD COLUMN IF NOT EXISTS skills TEXT[] NOT NULL DEFAULT '{}';

-- A Creator task born from an accepted suggestion, and why it went to that
-- Creator (the matching's score and reasons).
ALTER TABLE creator_tasks ADD COLUMN IF NOT EXISTS suggestion_id UUID REFERENCES suggestions(id) ON DELETE SET NULL;
ALTER TABLE creator_tasks ADD COLUMN IF NOT EXISTS match JSONB;
CREATE INDEX IF NOT EXISTS idx_creator_tasks_suggestion ON creator_tasks (suggestion_id);

-- The type of business (internal/business: servicios | restaurantes |
-- ventas, '' = none): the configuration on top of the signup's analysis.
-- Inferred at signup from the sectors or the description; the owner can
-- change it.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS business_type TEXT NOT NULL DEFAULT '';

-- The account's AndRho website (sites service on Railway): its host, and
-- whether it still shows "en construcción" or the Creator's site is live.
CREATE TABLE IF NOT EXISTS account_sites (
  account_id   UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  host         TEXT NOT NULL UNIQUE,
  status       TEXT NOT NULL DEFAULT 'construyendo', -- construyendo | publicado
  live_url     TEXT NOT NULL DEFAULT '',             -- the Creator's deployment, once published
  domain_state TEXT NOT NULL DEFAULT '',             -- what the domain setup did (dns, railway), for staff
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Plans from the catalog (internal/plans, todo.md C1): 'base' |
-- 'plataforma' | 'despegue' | 'en_orbita' | 'galaxy'. 'galactico' was
-- renamed 'galaxy' (the code still accepts the old id).
UPDATE accounts SET plan = 'galaxy' WHERE plan = 'galactico';

-- What the account bought (checkout, internal/billing): the plan, the
-- complementos (add-ons, [{feature_id, qty}]) and the monthly total in
-- quetzales. While payments are off (PAYMENTS_ENABLED=false) every account
-- is created with billing_status 'pending_payment' and payment_skipped =
-- true; db.ListPaymentSkipped lists them to charge or migrate them later.
-- accounts.plan stays the plan in force (PATCH /account/plan changes both).
CREATE TABLE IF NOT EXISTS account_plan (
  account_id       UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  plan             TEXT NOT NULL,
  addons           JSONB NOT NULL DEFAULT '[]',
  base_price       INTEGER NOT NULL DEFAULT 0,
  addons_total     INTEGER NOT NULL DEFAULT 0,
  total            INTEGER NOT NULL DEFAULT 0,
  currency         TEXT NOT NULL DEFAULT 'GTQ',
  billing_status   TEXT NOT NULL DEFAULT 'pending_payment', -- pending_payment | active | failed
  payment_skipped  BOOLEAN NOT NULL DEFAULT false,
  payment_provider TEXT NOT NULL DEFAULT '',
  payment_ref      TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_account_plan_skipped ON account_plan (payment_skipped) WHERE payment_skipped;
-- What's charged once (the website's construction, internal/plans site.go).
ALTER TABLE account_plan ADD COLUMN IF NOT EXISTS setup_total INTEGER NOT NULL DEFAULT 0;

-- Sites on the wildcard (todo.md Parte A): each client's site is its own
-- Railway service (site-<account_id>) in the Clients project, reached
-- through sites-router at <subdomain>.web.andrho.com.
--   status: offered (the plan doesn't include a site yet; no service) |
--           deploying (service being created or deployed) | live
--           (sites-router serves it). construyendo/publicado are the old
--           values (one shared sites service); MigrateLegacySites moves them.
--   subdomain: the reserved name ([a-z0-9-], ≤ 40, unique).
--   legacy_host: the old web.<id>.andrho.com, answered with a 301.
--   custom_domain: the client's own domain, once it has one (prepared, not
--           wired yet: it would be added to sites-router and the subdomain
--           would 301 to it).
ALTER TABLE account_sites ADD COLUMN IF NOT EXISTS subdomain TEXT;
ALTER TABLE account_sites ADD COLUMN IF NOT EXISTS railway_service_id TEXT NOT NULL DEFAULT '';
ALTER TABLE account_sites ADD COLUMN IF NOT EXISTS railway_service_name TEXT NOT NULL DEFAULT '';
ALTER TABLE account_sites ADD COLUMN IF NOT EXISTS legacy_host TEXT NOT NULL DEFAULT '';
ALTER TABLE account_sites ADD COLUMN IF NOT EXISTS custom_domain TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_sites_subdomain ON account_sites (subdomain) WHERE subdomain IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_account_sites_legacy ON account_sites (legacy_host) WHERE legacy_host <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_sites_custom ON account_sites (custom_domain) WHERE custom_domain <> '';

-- Each site's SEO (todo.md A4): metadata, Google Search Console's
-- verification meta tag (URL-prefix property, no DNS per client) and the
-- LocalBusiness data of each branch ([{name, address, phone, hours, lat, lng}]).
CREATE TABLE IF NOT EXISTS site_seo (
  account_id  UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  title       TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  og_image    TEXT NOT NULL DEFAULT '',
  lang        TEXT NOT NULL DEFAULT 'es-GT',
  gsc_token   TEXT NOT NULL DEFAULT '',
  branches    JSONB NOT NULL DEFAULT '[]',
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Browsers that asked AndRho to tell them when something is ready (Web
-- Push, internal/notify): the ERP after signup, for now.
CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint   TEXT PRIMARY KEY,
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_push_subscriptions_account ON push_subscriptions (account_id);

-- Venta en vivo (internal/live, founder/live-sale-spec.md). The seller's
-- Facebook Pages, connected with Facebook Login: the Page token is sealed
-- like the ERP password (internal/secret). One selected Page per account
-- sells; its events (comments, Messenger) reach POST /meta/webhook.
CREATE TABLE IF NOT EXISTS meta_pages (
  page_id      TEXT PRIMARY KEY,
  account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  picture      TEXT NOT NULL DEFAULT '',
  ig_user_id   TEXT NOT NULL DEFAULT '',
  token        BYTEA NOT NULL,
  selected     BOOLEAN NOT NULL DEFAULT false,
  subscribed   BOOLEAN NOT NULL DEFAULT false,
  connected_by UUID,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_meta_pages_account ON meta_pages (account_id);

-- A live sale: AndRho's side of the ERP's andrho.live.sale (odoo_live_id).
-- status: ready (organized) -> live (AndRho reads its comments) -> done.
CREATE TABLE IF NOT EXISTS live_sessions (
  id               UUID PRIMARY KEY,
  account_id       UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  odoo_live_id     INTEGER NOT NULL,
  page_id          TEXT NOT NULL DEFAULT '',
  platform_live_id TEXT NOT NULL DEFAULT '',
  live_url         TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'ready',
  last_comment_at  TIMESTAMPTZ,
  started_at       TIMESTAMPTZ,
  ended_at         TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_live_sessions_account ON live_sessions (account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_live_sessions_live ON live_sessions (status) WHERE status = 'live';
CREATE INDEX IF NOT EXISTS idx_live_sessions_platform ON live_sessions (platform_live_id) WHERE platform_live_id <> '';

-- Every comment that took (or queued for) an item: from the live
-- (source=live) or from an unsold-item post ("mío", source=post).
-- state: queued (someone else has it) -> asked (private reply sent) ->
-- confirmed | declined | expired | failed. One row per Meta comment.
CREATE TABLE IF NOT EXISTS live_claims (
  id           BIGSERIAL PRIMARY KEY,
  account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  session_id   UUID REFERENCES live_sessions(id) ON DELETE CASCADE,
  page_id      TEXT NOT NULL,
  odoo_line_id INTEGER NOT NULL,
  source       TEXT NOT NULL DEFAULT 'live',
  post_id      TEXT NOT NULL DEFAULT '',
  comment_id   TEXT NOT NULL UNIQUE,
  author_id    TEXT NOT NULL DEFAULT '',
  author_name  TEXT NOT NULL DEFAULT '',
  comment_text TEXT NOT NULL DEFAULT '',
  commented_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  state        TEXT NOT NULL DEFAULT 'queued',
  psid         TEXT NOT NULL DEFAULT '',
  card_id      INTEGER,
  asked_at     TIMESTAMPTZ,
  answered_at  TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_live_claims_line ON live_claims (account_id, odoo_line_id, state, commented_at);
CREATE INDEX IF NOT EXISTS idx_live_claims_session ON live_claims (session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_live_claims_asked ON live_claims (asked_at) WHERE state = 'asked';

-- Unsold items posted on the Page after a live; a "mío" there claims it.
CREATE TABLE IF NOT EXISTS live_posts (
  post_id      TEXT PRIMARY KEY,
  account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  session_id   UUID REFERENCES live_sessions(id) ON DELETE SET NULL,
  page_id      TEXT NOT NULL,
  odoo_line_id INTEGER NOT NULL,
  state        TEXT NOT NULL DEFAULT 'open',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A buyer's Messenger conversation after "Sí": which shipping detail
-- AndRho is waiting for (step: phone | address | change | '').
CREATE TABLE IF NOT EXISTS live_threads (
  page_id    TEXT NOT NULL,
  psid       TEXT NOT NULL,
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  step       TEXT NOT NULL DEFAULT '',
  card_id    INTEGER,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (page_id, psid)
);

-- Features an account turned on or off by hand (Configuración). A feature
-- is available when the plan includes it; without a row here it's on only
-- for the businesses that use it (live_sale: business type en_vivo).
CREATE TABLE IF NOT EXISTS account_features (
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  feature    TEXT NOT NULL,
  enabled    BOOLEAN NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, feature)
);
