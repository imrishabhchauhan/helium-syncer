# Helium Syncer — Spec v0.1 (20 Oct 2026)

Unofficial, free, open-source sync for the [Helium browser](https://helium.computer) on Windows and macOS.
Not affiliated with imput. Retire gracefully if Helium ships native sync ([imputnet/helium#90](https://github.com/imputnet/helium/issues/90)).

## 0. Design goals — for ANYONE with a Mac + Windows PC

- **10x easier:** one installer per OS, email code, done. No git, keys, servers or config files. The second device sets itself up (profiles, extensions, tabs). Plain-language status in the tray/menu bar ("Up to date · 2 min ago").
- **10x faster:** upload within seconds of quitting Helium; downloads applied in the background while Helium is closed, so opening Helium never waits on the network. Only changes travel, not whole files.
- **10x more reliable:** backup before every apply, item-level merges with tombstones, atomic writes, schema guards that pause instead of guessing, and an end-to-end test suite on real Helium on both OSes.

## 1. Why a helper app (not a browser fork, not a sync server)

- Helium is ungoogled-Chromium based: Chrome sign-in and the sync engine are disabled, so Chrome-protocol servers (selfsync, Brave go-sync) never receive traffic.
- Copying a profile folder between machines fails: `Secure Preferences` is HMAC-signed with a per-machine id, so Chromium resets settings and wipes extensions ("Some settings were reset").
- A fork would need constant rebasing on every Helium release.
- So: a small helper app reads/writes Helium's own profile files **only while Helium is closed**, and re-signs protected files on each device.

Verified on the owner's Windows box: Helium 155.0.8059.84, `User Data` at `%LOCALAPPDATA%\imput\Helium\User Data`, extensions in `Secure Preferences` (`protection.macs`, `super_mac`, `super_encrypted_hash`), `os_crypt` has only `encrypted_key` (DPAPI, no app-bound key).
Windows app is a system install at `C:\Program Files\imput\Helium\Application\chrome.exe`.
`protection.macs` now also holds `*_encrypted_hash` entries (homepage, pinned_tabs, session, search_provider_overrides…) next to the plain HMACs, plus `super_encrypted_hash` — the re-sign step must produce both (the encrypted ones use the profile's os_crypt key).
3 extensions are listed in `Secure Preferences` with 0 `Extensions/` folders, so built-in component extensions must be told apart from user-installed ones.
macOS path to confirm on the Mac (expected `~/Library/Application Support/net.imput.helium`) — run `scripts/check-mac.sh`.

## 2. User-facing behaviour

1. **Quit Helium** → helper captures changes, encrypts, uploads (seconds).
2. **Helium closed** on another device → helper downloads and applies changes in the background.
3. **Open Helium** → one last quick check (launcher shortcut), then Helium starts already up to date.
4. **Helium open** → helper never touches its files. Changes from elsewhere arrive at next open.
- macOS: "quit" = Cmd+Q (app process exits), not closing the last window.

First setup:
- Device 1 (already set up): install → email code → "Create sync" → uploads everything.
- Device 2: install → same email code → approve from device 1 (one click) → local profile backed up → **merged**, never erased. Missing profiles are created automatically.
- A printable/1Password-friendly **recovery key** for when no old device is available. Without old device AND recovery key, data can't be decrypted (by design — the server can't read it).

## 3. What syncs and how it merges

| Data | Source in profile | Merge rule |
|---|---|---|
| Profiles (name, colour, avatar) | `Local State` → `profile.info_cache`, profile dirs | Matched by a stable Syncer id (dir names can differ per device); missing ones created |
| Bookmarks | `<profile>/Bookmarks` (JSON, guid per node) | Item-level merge by guid; moves/renames last-writer-wins per field; deletes as tombstones; checksum recomputed |
| History | `History` (SQLite: `urls`, `visits`) | Union by (url, visit_time); deletions propagate as tombstones; ids remapped locally |
| Open tabs, pinned tabs, live tab groups | `Sessions/Session_*`, `Tabs_*` (SNSS) | **Combine**: dedupe by URL per window/group; a tab closed on device A stays closed unless B used it after A's close; pinned tabs matched by URL; groups matched by id, then name+colour |
| Saved tab groups | `Sync Data/LevelDB` (`saved_tab_group-dt-<UUID>`) | Merge by UUID, tombstones |
| Settings | `Preferences` (allow-list) + `Secure Preferences` | Per-key last-writer-wins; device-specific keys excluded (download dir, window placement, GPU, paths); protected keys re-signed with the target device's id |
| Search engines | `Web Data` → `keywords` | Merge by keyword/GUID |
| Extensions | `Extensions/<id>/<ver>`, `Secure Preferences` → `extensions.settings` | Install set = union minus explicit uninstalls; registered + re-signed on target |
| Extension data | `Local Extension Settings/<id>`, `Sync Extension Settings/<id>`, `IndexedDB/chrome-extension_<id>*`, extension origins in `Local Storage` | Per-extension last-writer-wins (whole store), with backups |
| Logins (cookies) | `Network/Cookies` (encrypted) | **Off by default, opt-in per site.** Decrypt with source OS key, re-encrypt with target OS key. Google/Microsoft/Apple/banks/payments locked off unless the user unlocks with a warning. Never live on two devices at once |
| Passwords | — | Not synced; recommend 1Password/Bitwarden extension |
| Never synced | caches, GPU/shader caches, crash data, `Network Action Predictor`, site engagement | — |

## 4. Reliability rules

- Never write while Helium runs (process check + profile `LOCK` file).
- Full profile backup before every apply; keep 30 days; one-click restore.
- Atomic writes (temp file → fsync → rename).
- Change records with item ids, hybrid logical clocks and tombstones — no whole-file overwrites.
- Format guard: each reader/writer is pinned to known Helium/Chromium schema versions; unknown version → that data type pauses and the app warns, never guesses.
- Dry-run + diff view available for every apply.

## 5. Architecture

- **Helper app** (Windows + macOS): Go, single binary, no CGO (`modernc.org/sqlite`, `syndtr/goleveldb`). UI via Wails (tray/menu bar + onboarding). Background agent at login (Windows Task Scheduler / macOS LaunchAgent). Launcher shortcut for the "last check on open".
- **Server** (Cloudflare, owner-hosted, self-host optional): Worker (TypeScript) + Durable Object per account (ordering, live notify) + D1 (accounts, devices, change index) + R2 (encrypted blobs, snapshots). Email codes via Cloudflare Email Service.
- **Encryption**: random account key; XChaCha20-Poly1305 for all payloads; device keys X25519; new device gets the account key sealed by an existing device; recovery key as fallback. Server sees only ciphertext + sizes.
- **Free-tier guard**: per-account storage caps, history compaction, snapshot + delta log with periodic compaction.

## 6. Testing ("hyper testing")

- Unit + golden-file tests per format (Bookmarks, History, SNSS, LevelDB, Secure Preferences MACs).
- End-to-end: drive real Helium with throwaway `--user-data-dir` profiles via the DevTools protocol on both OSes — open tabs/pins/groups, bookmark, browse, install extensions → quit → sync → open on the other device → verify.
- Edge cases: both devices open at once, one offline for weeks, power cut mid-apply, Helium auto-update between syncs, 10k bookmarks / 100k history / 300 tabs, profile renamed, extension removed on one side.
- Never test on the owner's real profile until all of the above pass.

## 7. Phases

0. Format proof on Windows (read/write every data type on a throwaway profile, Helium 155).
1. Core: accounts, encryption, profiles, bookmarks, history, tabs/pins/groups, settings.
2. Extensions + extension data.
3. Per-site logins (opt-in).
4. Public release: signed installers (Apple Developer ID + notarization, Windows signing), auto-update, website, privacy policy, self-host guide.

## 8. Open items

- Mac access for building/testing the macOS side.
- Name check: "Helium Syncer" uses imput's product name — ship with "Unofficial, not affiliated with Helium/imput"; rename if asked.
- GitHub repo `imrishabhchauhan/helium-syncer` (public) — create when the owner says go.
