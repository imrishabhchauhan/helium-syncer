// Helium Syncer server (Cloudflare Worker).
//
// The server is an ordered mailbox for end-to-end encrypted op batches plus
// a small device list. There are no emails or passwords: a device joins an
// account by proving it knows the account's sync code (it sends a secret
// derived from the code; the server keeps only its SHA-256). The server
// never sees the code or the encryption key, so it cannot read anything.

export interface Env {
  DB: D1Database;
  BLOBS: R2Bucket;
}

const MAX_ENVELOPE = 512 * 1024; // bytes of ciphertext per batch
const MAX_BLOB = 25 * 1024 * 1024;
const ACCOUNT_QUOTA = 500 * 1024 * 1024;
const HEX64 = /^[0-9a-f]{64}$/;

class HttpError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json; charset=utf-8" } });

const hex = (buf: ArrayBuffer) => [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");

async function sha256Hex(data: string | Uint8Array): Promise<string> {
  const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
  return hex(await crypto.subtle.digest("SHA-256", bytes));
}

function randomToken(bytes = 32): string {
  const b = crypto.getRandomValues(new Uint8Array(bytes));
  return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

async function body<T>(req: Request): Promise<T> {
  try {
    return (await req.json()) as T;
  } catch {
    throw new HttpError(400, "bad_json", "The request body is not valid JSON.");
  }
}

function str(v: unknown, field: string, max: number): string {
  const s = String(v ?? "").trim();
  if (!s || s.length > max) throw new HttpError(400, "bad_" + field, `Missing or too long: ${field}.`);
  return s;
}

// --- rate limits -----------------------------------------------------------

async function limit(env: Env, kind: string, subject: string, max: number, windowMs: number) {
  const now = Date.now();
  const win = Math.floor(now / windowMs);
  const row = await env.DB.prepare(
    "INSERT INTO rate (key, count, expires) VALUES (?1, 1, ?2) ON CONFLICT(key) DO UPDATE SET count = count + 1 RETURNING count",
  )
    .bind(`${kind}:${subject}:${win}`, (win + 1) * windowMs)
    .first<{ count: number }>();
  if ((row?.count ?? 0) > max) {
    throw new HttpError(429, "rate_limited", "Too many attempts. Please wait a few minutes and try again.");
  }
  if (Math.random() < 0.01) await env.DB.prepare("DELETE FROM rate WHERE expires < ?1").bind(now).run();
}

// --- devices -----------------------------------------------------------------

interface DeviceRow {
  id: string;
  account_id: string;
  name: string;
  platform: string;
  status: string;
}

async function authed(req: Request, env: Env): Promise<DeviceRow> {
  const h = req.headers.get("authorization") ?? "";
  const token = h.startsWith("Bearer ") ? h.slice(7) : "";
  if (!token) throw new HttpError(401, "unauthorized", "This device is not connected. Enter your sync code again.");
  const dev = await env.DB.prepare("SELECT id, account_id, name, platform, status FROM devices WHERE token_hash = ?1")
    .bind(await sha256Hex(token))
    .first<DeviceRow>();
  if (!dev || dev.status !== "active") {
    throw new HttpError(401, "unauthorized", "This device was removed from the sync. Enter your sync code to join again.");
  }
  await env.DB.prepare("UPDATE devices SET last_seen = ?1 WHERE id = ?2").bind(Date.now(), dev.id).run();
  return dev;
}

// POST /v1/join — create a sync (first device) or join one (any other device).
async function join(req: Request, env: Env) {
  const b = await body<{ account_id: string; auth: string; create: boolean; device: { name: string; platform: string } }>(req);
  const ip = req.headers.get("cf-connecting-ip") ?? "local";
  await limit(env, "join-ip", ip, 30, 60 * 60 * 1000);
  const accountID = String(b.account_id ?? "");
  const auth = String(b.auth ?? "");
  if (!HEX64.test(accountID) || !HEX64.test(auth)) throw new HttpError(400, "bad_code", "That sync code is not valid.");
  const name = str(b.device?.name, "device_name", 80);
  const platform = str(b.device?.platform, "platform", 16);
  const verifier = await sha256Hex(auth);
  const now = Date.now();

  const acct = await env.DB.prepare("SELECT verifier FROM accounts WHERE id = ?1").bind(accountID).first<{ verifier: string }>();
  let created = false;
  if (!acct) {
    if (!b.create) {
      throw new HttpError(404, "no_sync", "No sync uses this code. Check the code, or start a new sync on this device.");
    }
    await env.DB.prepare("INSERT INTO accounts (id, verifier, created_at) VALUES (?1, ?2, ?3)").bind(accountID, verifier, now).run();
    created = true;
  } else if (!timingSafeEqual(acct.verifier, verifier)) {
    await limit(env, "join-bad", accountID, 10, 60 * 60 * 1000);
    throw new HttpError(403, "wrong_code", "That sync code is not right.");
  }
  const token = randomToken();
  const deviceID = crypto.randomUUID();
  await env.DB.prepare(
    "INSERT INTO devices (id, account_id, name, platform, token_hash, status, created_at, last_seen) VALUES (?1, ?2, ?3, ?4, ?5, 'active', ?6, ?6)",
  )
    .bind(deviceID, accountID, name, platform, await sha256Hex(token), now)
    .run();
  return json({ device_id: deviceID, token, created });
}

async function listDevices(req: Request, env: Env) {
  const me = await authed(req, env);
  const rows = await env.DB.prepare(
    "SELECT id, name, platform, helium_version, created_at, last_seen FROM devices WHERE account_id = ?1 AND status = 'active' ORDER BY created_at",
  )
    .bind(me.account_id)
    .all();
  return json({ me: me.id, devices: rows.results });
}

async function removeDevice(req: Request, env: Env, id: string) {
  const me = await authed(req, env);
  const r = await env.DB.prepare("UPDATE devices SET status = 'removed' WHERE id = ?1 AND account_id = ?2 AND status = 'active'")
    .bind(id, me.account_id)
    .run();
  if (!r.meta.changes) throw new HttpError(404, "not_found", "No such device.");
  return json({ removed: true });
}

async function announce(req: Request, env: Env) {
  const me = await authed(req, env);
  const b = await body<{ helium_version: string; name?: string }>(req);
  await env.DB.prepare("UPDATE devices SET helium_version = ?1, name = COALESCE(?2, name) WHERE id = ?3")
    .bind(str(b.helium_version, "helium_version", 40), b.name ? str(b.name, "device_name", 80) : null, me.id)
    .run();
  return json({ saved: true });
}

// --- envelopes ---------------------------------------------------------------

async function pushEnvelopes(req: Request, env: Env) {
  const me = await authed(req, env);
  const b = await body<{ items: { nonce: string; ciphertext: string }[] }>(req);
  if (!Array.isArray(b.items) || b.items.length === 0 || b.items.length > 50) {
    throw new HttpError(400, "bad_items", "Send between 1 and 50 batches at a time.");
  }
  let total = 0;
  for (const it of b.items) {
    const size = String(it.ciphertext ?? "").length;
    if (!it.nonce || size === 0 || size > MAX_ENVELOPE * 1.4) throw new HttpError(413, "too_big", "A batch is too large.");
    total += size;
  }
  const acct = await env.DB.prepare("SELECT storage FROM accounts WHERE id = ?1").bind(me.account_id).first<{ storage: number }>();
  if ((acct?.storage ?? 0) + total > ACCOUNT_QUOTA) throw new HttpError(507, "quota", "Your sync storage is full.");
  const now = Date.now();
  const stmts = b.items.map((it) =>
    env.DB.prepare(
      "INSERT INTO envelopes (account_id, device_id, nonce, ciphertext, size, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6) RETURNING seq",
    ).bind(me.account_id, me.id, it.nonce, it.ciphertext, it.ciphertext.length, now),
  );
  stmts.push(env.DB.prepare("UPDATE accounts SET storage = storage + ?1 WHERE id = ?2").bind(total, me.account_id));
  const res = await env.DB.batch(stmts);
  const seqs = res.slice(0, b.items.length).map((r) => (r.results?.[0] as { seq: number }).seq);
  return json({ seqs });
}

async function pullEnvelopes(req: Request, env: Env, url: URL) {
  const me = await authed(req, env);
  const after = Math.max(0, Number(url.searchParams.get("after") ?? 0) || 0);
  const lim = Math.min(200, Math.max(1, Number(url.searchParams.get("limit") ?? 100) || 100));
  const rows = await env.DB.prepare(
    "SELECT seq, device_id, nonce, ciphertext FROM envelopes WHERE account_id = ?1 AND seq > ?2 ORDER BY seq LIMIT ?3",
  )
    .bind(me.account_id, after, lim)
    .all<{ seq: number }>();
  const items = rows.results;
  return json({ items, next: items.length ? items[items.length - 1].seq : after, more: items.length === lim });
}

// --- blobs -------------------------------------------------------------------

async function putBlob(req: Request, env: Env, hash: string) {
  const me = await authed(req, env);
  const data = new Uint8Array(await req.arrayBuffer());
  if (data.length === 0 || data.length > MAX_BLOB) throw new HttpError(413, "too_big", "This file is too large to sync.");
  if ((await sha256Hex(data)) !== hash) throw new HttpError(400, "hash_mismatch", "The upload was damaged. Try again.");
  const exists = await env.DB.prepare("SELECT 1 FROM blobs WHERE account_id = ?1 AND hash = ?2").bind(me.account_id, hash).first();
  if (!exists) {
    const acct = await env.DB.prepare("SELECT storage FROM accounts WHERE id = ?1").bind(me.account_id).first<{ storage: number }>();
    if ((acct?.storage ?? 0) + data.length > ACCOUNT_QUOTA) throw new HttpError(507, "quota", "Your sync storage is full.");
    await env.BLOBS.put(`${me.account_id}/${hash}`, data);
    await env.DB.batch([
      env.DB.prepare("INSERT INTO blobs (account_id, hash, size, created_at) VALUES (?1, ?2, ?3, ?4)").bind(me.account_id, hash, data.length, Date.now()),
      env.DB.prepare("UPDATE accounts SET storage = storage + ?1 WHERE id = ?2").bind(data.length, me.account_id),
    ]);
  }
  return json({ stored: true });
}

async function getBlob(req: Request, env: Env, hash: string) {
  const me = await authed(req, env);
  const obj = await env.BLOBS.get(`${me.account_id}/${hash}`);
  if (!obj) throw new HttpError(404, "not_found", "File not found.");
  return new Response(obj.body, { headers: { "content-type": "application/octet-stream" } });
}

// --- router ------------------------------------------------------------------

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const p = url.pathname;
    const m = req.method;
    try {
      if (m === "GET" && p === "/") {
        return new Response(
          "Helium Syncer server. Unofficial, open source, not affiliated with Helium or imput. https://github.com/imrishabhchauhan/helium-syncer\n",
          { headers: { "content-type": "text/plain; charset=utf-8" } },
        );
      }
      if (m === "GET" && p === "/v1/health") return json({ ok: true });
      if (m === "POST" && p === "/v1/join") return await join(req, env);
      if (m === "GET" && p === "/v1/devices") return await listDevices(req, env);
      if (m === "PUT" && p === "/v1/devices/me") return await announce(req, env);
      let mm = p.match(/^\/v1\/devices\/([0-9a-f-]{36})\/remove$/);
      if (m === "POST" && mm) return await removeDevice(req, env, mm[1]);
      if (m === "POST" && p === "/v1/envelopes") return await pushEnvelopes(req, env);
      if (m === "GET" && p === "/v1/envelopes") return await pullEnvelopes(req, env, url);
      mm = p.match(/^\/v1\/blobs\/([0-9a-f]{64})$/);
      if (mm && m === "PUT") return await putBlob(req, env, mm[1]);
      if (mm && m === "GET") return await getBlob(req, env, mm[1]);
      throw new HttpError(404, "not_found", "Not found.");
    } catch (err) {
      if (err instanceof HttpError) return json({ error: err.code, message: err.message }, err.status);
      console.error(err);
      return json({ error: "internal", message: "Something went wrong on the server. Please try again." }, 500);
    }
  },
};
