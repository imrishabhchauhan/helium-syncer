-- Helium Syncer server schema (Cloudflare D1).
-- There are no emails or passwords. An account is identified by an id
-- derived from its sync code; the server stores only the SHA-256 of the
-- auth secret derived from the same code, plus ciphertext.

CREATE TABLE IF NOT EXISTS accounts (
  id          TEXT PRIMARY KEY,           -- hex, derived from the sync code
  verifier    TEXT NOT NULL,              -- sha256(auth secret), hex
  created_at  INTEGER NOT NULL,
  storage     INTEGER NOT NULL DEFAULT 0  -- bytes stored (envelopes + blobs)
);

CREATE TABLE IF NOT EXISTS devices (
  id          TEXT PRIMARY KEY,
  account_id  TEXT NOT NULL REFERENCES accounts(id),
  name        TEXT NOT NULL,
  platform    TEXT NOT NULL,              -- windows | macos | linux
  token_hash  TEXT NOT NULL UNIQUE,       -- sha256 of the device token (hex)
  status      TEXT NOT NULL,              -- active | removed
  helium_version TEXT,
  created_at  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS devices_account ON devices(account_id);

-- Fixed-window counters for rate limits (key = kind:subject:window).
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
