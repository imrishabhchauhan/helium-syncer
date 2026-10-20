-- Helium Syncer server schema (Cloudflare D1).
-- The server stores only ciphertext for synced data. It knows account
-- emails (for sign-in codes), device names and Helium versions.

CREATE TABLE IF NOT EXISTS accounts (
  id          TEXT PRIMARY KEY,
  email       TEXT NOT NULL UNIQUE,      -- lower-case
  created_at  INTEGER NOT NULL,
  storage     INTEGER NOT NULL DEFAULT 0, -- bytes stored (envelopes + blobs)
  recovery_blob     TEXT,                 -- account key wrapped by the recovery key (base64)
  recovery_verifier TEXT                  -- sha256 of the recovery auth secret (hex)
);

CREATE TABLE IF NOT EXISTS devices (
  id          TEXT PRIMARY KEY,
  account_id  TEXT NOT NULL REFERENCES accounts(id),
  name        TEXT NOT NULL,
  platform    TEXT NOT NULL,              -- windows | macos | linux
  public_key  TEXT NOT NULL,              -- X25519, base64
  token_hash  TEXT NOT NULL UNIQUE,       -- sha256 of the device token (hex)
  status      TEXT NOT NULL,              -- approved | pending | removed
  sealed_key  TEXT,                       -- account key sealed to public_key (base64)
  helium_version TEXT,
  created_at  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS devices_account ON devices(account_id);

CREATE TABLE IF NOT EXISTS auth_codes (
  email       TEXT PRIMARY KEY,
  salt        TEXT NOT NULL,
  code_hash   TEXT NOT NULL,              -- sha256(salt || code)
  expires_at  INTEGER NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0
);

-- Sliding-window counters for rate limits (key = kind:subject:window).
CREATE TABLE IF NOT EXISTS rate (
  key     TEXT PRIMARY KEY,
  count   INTEGER NOT NULL,
  expires INTEGER NOT NULL
);

-- Encrypted op batches, in the order the server accepted them.
CREATE TABLE IF NOT EXISTS envelopes (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id  TEXT NOT NULL,
  device_id   TEXT NOT NULL,
  nonce       TEXT NOT NULL,              -- base64
  ciphertext  TEXT NOT NULL,              -- base64
  size        INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS envelopes_account_seq ON envelopes(account_id, seq);

-- Encrypted blobs (extension packages and data) live in R2; this indexes them.
CREATE TABLE IF NOT EXISTS blobs (
  account_id  TEXT NOT NULL,
  hash        TEXT NOT NULL,              -- sha256 of the ciphertext (hex)
  size        INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (account_id, hash)
);
