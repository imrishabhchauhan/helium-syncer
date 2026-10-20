// Helium Syncer server (Cloudflare Worker).
//
// The server is a dumb, ordered mailbox for end-to-end encrypted op batches
// plus a small device registry. It never sees the account key, so it cannot
// read anyone's bookmarks, history or tabs.

export interface Env {
  DB: D1Database;
  BLOBS: R2Bucket;
  EMAIL?: SendEmail; // Cloudflare Email Sending binding (MAIL_MODE=cloudflare)
  MAIL_MODE: "log" | "resend" | "cloudflare";
  MAIL_FROM: string; // e.g. no-reply@aiwithrishabh.com
  RESEND_API_KEY?: string; // secret, MAIL_MODE=resend
}

const MAX_ENVELOPE = 512 * 1024; // bytes of ciphertext per batch
const MAX_BLOB = 25 * 1024 * 1024;
const ACCOUNT_QUOTA = 500 * 1024 * 1024;
const CODE_TTL = 10 * 60 * 1000;
const MAX_CODE_ATTEMPTS = 5;

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

function randomCode(): string {
  // Uniform 6-digit code without modulo bias.
  const max = 4_294_000_000; // largest multiple of 1e6 below 2^32
  for (;;) {
    const n = crypto.getRandomValues(new Uint32Array(1))[0];
    if (n < max) return String(n % 1_000_000).padStart(6, "0");
  }
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

const EMAIL_RE = /^[^\s@]{1,64}@[^\s@]{1,255}\.[^\s@]{2,63}$/;

function normEmail(e: unknown): string {
  const s = String(e ?? "").trim().toLowerCase();
  if (!EMAIL_RE.test(s)) throw new HttpError(400, "bad_email", "Enter a valid email address.");
  return s;
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
  const key = `${kind}:${subject}:${win}`;
  const row = await env.DB.prepare(
    "INSERT INTO rate (key, count, expires) VALUES (?1, 1, ?2) ON CONFLICT(key) DO UPDATE SET count = count + 1 RETURNING count",
  )
    .bind(key, (win + 1) * windowMs)
    .first<{ count: number }>();
  if ((row?.count ?? 0) > max) {
    throw new HttpError(429, "rate_limited", "Too many attempts. Please wait a few minutes and try again.");
  }
  if (Math.random() < 0.01) await env.DB.prepare("DELETE FROM rate WHERE expires < ?1").bind(now).run();
}

// --- email -----------------------------------------------------------------

async function sendCode(env: Env, to: string, code: string) {
  const subject = `${code} is your Helium Syncer code`;
  const text =
    `Your Helium Syncer sign-in code is ${code}\n\n` +
    `It expires in 10 minutes. If you did not ask for it, you can ignore this email.\n\n` +
    `Helium Syncer is an unofficial, open-source project and is not affiliated with Helium or imput.`;
  const html =
    `<div style="font-family:system-ui,sans-serif;font-size:15px;color:#1f2328;max-width:420px">` +
    `<p>Your Helium Syncer sign-in code is</p>` +
    `<p style="font-size:28px;letter-spacing:6px;font-weight:600;margin:8px 0 16px">${code}</p>` +
    `<p style="color:#59636e">It expires in 10 minutes. If you did not ask for it, you can ignore this email.</p>` +
    `<p style="color:#59636e;font-size:12px">Helium Syncer is an unofficial, open-source project and is not affiliated with Helium or imput.</p></div>`;
  switch (env.MAIL_MODE) {
    case "log":
      // Local development only: never send real email from a dev server.
      console.log(`[dev mail] to=${to} code=${code}`);
      return;
    case "resend": {
      if (!env.RESEND_API_KEY) throw new HttpError(500, "mail_not_configured", "Email is not configured on this server.");
      const r = await fetch("https://api.resend.com/emails", {
        method: "POST",
        headers: { authorization: `Bearer ${env.RESEND_API_KEY}`, "content-type": "application/json" },
        body: JSON.stringify({ from: `Helium Syncer <${env.MAIL_FROM}>`, to: [to], subject, text, html }),
      });
      if (!r.ok) throw new HttpError(502, "mail_failed", "We could not send the email. Please try again in a minute.");
      return;
    }
    case "cloudflare":
      if (!env.EMAIL) throw new HttpError(500, "mail_not_configured", "Email is not configured on this server.");
      await env.EMAIL.send({ to, from: { email: env.MAIL_FROM, name: "Helium Syncer" }, subject, text, html });
      return;
  }
}

// --- auth ------------------------------------------------------------------

interface DeviceRow {
  id: string;
  account_id: string;
  name: string;
  platform: string;
  public_key: string;
  status: string;
  sealed_key: string | null;
  helium_version: string | null;
  created_at: number;
  last_seen: number;
}

async function authed(req: Request, env: Env, needApproved = true): Promise<DeviceRow> {
  const h = req.headers.get("authorization") ?? "";
  const token = h.startsWith("Bearer ") ? h.slice(7) : "";
  if (!token) throw new HttpError(401, "unauthorized", "Sign in again.");
  const dev = await env.DB.prepare("SELECT * FROM devices WHERE token_hash = ?1").bind(await sha256Hex(token)).first<DeviceRow>();
  if (!dev || dev.status === "removed") throw new HttpError(401, "unauthorized", "This device was signed out. Sign in again.");
  if (needApproved && dev.status !== "approved") {
    throw new HttpError(403, "pending", "Approve this device from one of your other devices first.");
  }
  await env.DB.prepare("UPDATE devices SET last_seen = ?1 WHERE id = ?2").bind(Date.now(), dev.id).run();
  return dev;
}

async function authStart(req: Request, env: Env) {
  const { email } = await body<{ email: string }>(req);
  const e = normEmail(email);
  const ip = req.headers.get("cf-connecting-ip") ?? "local";
  await limit(env, "code-ip", ip, 20, 60 * 60 * 1000);
  await limit(env, "code-email", e, 5, 60 * 60 * 1000);
  const code = randomCode();
  const salt = randomToken(16);
  await env.DB.prepare(
    "INSERT INTO auth_codes (email, salt, code_hash, expires_at, attempts) VALUES (?1, ?2, ?3, ?4, 0) " +
      "ON CONFLICT(email) DO UPDATE SET salt = ?2, code_hash = ?3, expires_at = ?4, attempts = 0",
  )
    .bind(e, salt, await sha256Hex(salt + code), Date.now() + CODE_TTL)
    .run();
  await sendCode(env, e, code);
  return json({ sent: true });
}

async function authVerify(req: Request, env: Env) {
  const b = await body<{ email: string; code: string; device: { name: string; platform: string; public_key: string } }>(req);
  const e = normEmail(b.email);
  const code = String(b.code ?? "").replace(/\s/g, "");
  const ip = req.headers.get("cf-connecting-ip") ?? "local";
  await limit(env, "verify-ip", ip, 60, 60 * 60 * 1000);
  const row = await env.DB.prepare("SELECT * FROM auth_codes WHERE email = ?1").bind(e).first<{
    salt: string;
    code_hash: string;
    expires_at: number;
    attempts: number;
  }>();
  if (!row || row.expires_at < Date.now()) throw new HttpError(400, "code_expired", "This code has expired. Ask for a new one.");
  if (row.attempts >= MAX_CODE_ATTEMPTS) throw new HttpError(400, "code_locked", "Too many wrong codes. Ask for a new one.");
  if (!/^\d{6}$/.test(code) || !timingSafeEqual(await sha256Hex(row.salt + code), row.code_hash)) {
    await env.DB.prepare("UPDATE auth_codes SET attempts = attempts + 1 WHERE email = ?1").bind(e).run();
    throw new HttpError(400, "code_wrong", "That code is not right. Check the latest email and try again.");
  }
  await env.DB.prepare("DELETE FROM auth_codes WHERE email = ?1").bind(e).run();

  const name = str(b.device?.name, "device_name", 80);
  const platform = str(b.device?.platform, "platform", 16);
  const pub = str(b.device?.public_key, "public_key", 100);
  const now = Date.now();
  let account = await env.DB.prepare("SELECT id FROM accounts WHERE email = ?1").bind(e).first<{ id: string }>();
  let first = false;
  if (!account) {
    account = { id: crypto.randomUUID() };
    await env.DB.prepare("INSERT INTO accounts (id, email, created_at) VALUES (?1, ?2, ?3)").bind(account.id, e, now).run();
    first = true;
  } else {
    const live = await env.DB.prepare("SELECT COUNT(*) AS n FROM devices WHERE account_id = ?1 AND status = 'approved'")
      .bind(account.id)
      .first<{ n: number }>();
    first = (live?.n ?? 0) === 0 && !(await env.DB.prepare("SELECT recovery_blob FROM accounts WHERE id = ?1").bind(account.id).first<{ recovery_blob: string | null }>())?.recovery_blob;
  }
  const token = randomToken();
  const deviceID = crypto.randomUUID();
  // The first device of a brand-new account creates the account key, so it
  // is approved at once. Every later device must be approved by an existing
  // device (or with the recovery key).
  const status = first ? "approved" : "pending";
  await env.DB.prepare(
    "INSERT INTO devices (id, account_id, name, platform, public_key, token_hash, status, created_at, last_seen) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?8)",
  )
    .bind(deviceID, account.id, name, platform, pub, await sha256Hex(token), status, now)
    .run();
  return json({ account_id: account.id, device_id: deviceID, token, status, new_account: first });
}

// --- devices & keys ----------------------------------------------------------

async function listDevices(req: Request, env: Env) {
  const me = await authed(req, env, false);
  const rows = await env.DB.prepare(
    "SELECT id, name, platform, public_key, status, helium_version, created_at, last_seen FROM devices WHERE account_id = ?1 AND status != 'removed' ORDER BY created_at",
  )
    .bind(me.account_id)
    .all();
  return json({ me: me.id, devices: rows.results });
}

async function approveDevice(req: Request, env: Env, id: string) {
  const me = await authed(req, env);
  const { sealed_key } = await body<{ sealed_key: string }>(req);
  const sk = str(sealed_key, "sealed_key", 400);
  const r = await env.DB.prepare(
    "UPDATE devices SET status = 'approved', sealed_key = ?1 WHERE id = ?2 AND account_id = ?3 AND status = 'pending'",
  )
    .bind(sk, id, me.account_id)
    .run();
  if (!r.meta.changes) throw new HttpError(404, "not_found", "No device waiting for approval with that id.");
  return json({ approved: true });
}

async function removeDevice(req: Request, env: Env, id: string) {
  const me = await authed(req, env);
  if (id === me.id) throw new HttpError(400, "self", "Use sign out on this device instead.");
  const r = await env.DB.prepare("UPDATE devices SET status = 'removed', sealed_key = NULL WHERE id = ?1 AND account_id = ?2")
    .bind(id, me.account_id)
    .run();
  if (!r.meta.changes) throw new HttpError(404, "not_found", "No such device.");
  return json({ removed: true });
}

async function myKey(req: Request, env: Env) {
  const me = await authed(req, env, false);
  if (me.status !== "approved" || !me.sealed_key) {
    return json({ status: me.status }, me.status === "approved" ? 200 : 202);
  }
  return json({ status: "approved", sealed_key: me.sealed_key });
}

async function setMyKey(req: Request, env: Env) {
  // The first device stores its own sealed copy so it can recover after a reinstall.
  const me = await authed(req, env);
  const { sealed_key } = await body<{ sealed_key: string }>(req);
  await env.DB.prepare("UPDATE devices SET sealed_key = ?1 WHERE id = ?2").bind(str(sealed_key, "sealed_key", 400), me.id).run();
  return json({ saved: true });
}

async function announce(req: Request, env: Env) {
  const me = await authed(req, env, false);
  const { helium_version } = await body<{ helium_version: string }>(req);
  await env.DB.prepare("UPDATE devices SET helium_version = ?1 WHERE id = ?2").bind(str(helium_version, "helium_version", 40), me.id).run();
  return json({ saved: true });
}

async function putRecovery(req: Request, env: Env) {
  const me = await authed(req, env);
  const b = await body<{ blob: string; verifier: string }>(req);
  await env.DB.prepare("UPDATE accounts SET recovery_blob = ?1, recovery_verifier = ?2 WHERE id = ?3")
    .bind(str(b.blob, "blob", 400), str(b.verifier, "verifier", 64), me.account_id)
    .run();
  return json({ saved: true });
}

async function claimRecovery(req: Request, env: Env) {
  // A pending device proves it has the recovery key and is approved.
  const me = await authed(req, env, false);
  await limit(env, "recovery", me.account_id, 10, 60 * 60 * 1000);
  const { secret } = await body<{ secret: string }>(req);
  const acct = await env.DB.prepare("SELECT recovery_blob, recovery_verifier FROM accounts WHERE id = ?1")
    .bind(me.account_id)
    .first<{ recovery_blob: string | null; recovery_verifier: string | null }>();
  if (!acct?.recovery_verifier || !acct.recovery_blob) throw new HttpError(404, "no_recovery", "This account has no recovery key set up.");
  if (!timingSafeEqual(await sha256Hex(str(secret, "secret", 200)), acct.recovery_verifier)) {
    throw new HttpError(403, "recovery_wrong", "That recovery key is not right.");
  }
  await env.DB.prepare("UPDATE devices SET status = 'approved' WHERE id = ?1").bind(me.id).run();
  return json({ blob: acct.recovery_blob });
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
  if (!/^[0-9a-f]{64}$/.test(hash)) throw new HttpError(400, "bad_hash", "Bad blob id.");
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
  if (!/^[0-9a-f]{64}$/.test(hash)) throw new HttpError(400, "bad_hash", "Bad blob id.");
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
      if (m === "POST" && p === "/v1/auth/start") return await authStart(req, env);
      if (m === "POST" && p === "/v1/auth/verify") return await authVerify(req, env);
      if (m === "GET" && p === "/v1/devices") return await listDevices(req, env);
      if (m === "PUT" && p === "/v1/devices/me") return await announce(req, env);
      let mm = p.match(/^\/v1\/devices\/([0-9a-f-]{36})\/(approve|remove)$/);
      if (m === "POST" && mm) return mm[2] === "approve" ? await approveDevice(req, env, mm[1]) : await removeDevice(req, env, mm[1]);
      if (m === "GET" && p === "/v1/keys/mine") return await myKey(req, env);
      if (m === "PUT" && p === "/v1/keys/mine") return await setMyKey(req, env);
      if (m === "PUT" && p === "/v1/recovery") return await putRecovery(req, env);
      if (m === "POST" && p === "/v1/recovery/claim") return await claimRecovery(req, env);
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
