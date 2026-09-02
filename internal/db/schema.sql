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

CREATE TABLE IF NOT EXISTS refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_account ON refresh_tokens (account_id);
