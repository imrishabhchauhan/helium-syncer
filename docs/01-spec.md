# Helium Syncer — Spec v0.1 (20 Oct 2026)

Unofficial, free, open-source sync for the [Helium browser](https://helium.computer) on Windows and macOS.
Not affiliated with imput. Retire gracefully if Helium ships native sync ([imputnet/helium#90](https://github.com/imputnet/helium/issues/90)).

## 0. Design goals — for ANYONE with a Mac + Windows PC

- **10x easier:** one installer per OS, one sync code, done (no account, email or password). No git, keys, servers or config files. The second device sets itself up (profiles, extensions, tabs). Plain-language status in the tray/menu bar ("Up to date · 2 min ago").
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

## 1b. Works on every setup (detect, never assume)

Every user's machine is different: install location, per-user vs machine-wide, custom profile folders, Intel vs Apple Silicon, x64 vs ARM64, different OS and Helium versions. Rules:

- **Find Helium by identity, not by path.**
  - Windows: Uninstall registry entries (`HKLM`, `WOW6432Node`, `HKCU`) with `DisplayName = Helium` / `Publisher = The Helium Authors`, `StartMenuInternet\Helium`, running processes signed/described as Helium, Scoop/winget/portable folders. Never trust `App Paths\chrome.exe` (on the owner's PC it points to Helium even though Google Chrome is also installed).
  - macOS: LaunchServices / Spotlight by bundle id `net.imput.helium` (any folder, any disk), plus `/Applications` and `~/Applications`.
- **Find the profile folder the same way:** default location, plus any `--user-data-dir` from the running process, shortcuts or policies. Several user data folders on one machine are allowed; each is synced separately.
- **Read the version from the data, not the app:** `Last Version` + `Local State` → pick the matching reader/writer; unknown version → pause that data type with a clear message.
- **Platform differences are data, not code branches scattered around** — one table per OS:

| Item | Windows | macOS |
|---|---|---|
| User data | `%LOCALAPPDATA%\imput\Helium\User Data` (default) | `~/Library/Application Support/net.imput.helium` (default) |
| App | `Program Files\imput\Helium\Application\chrome.exe` (machine-wide) or `%LOCALAPPDATA%\…` (per-user) | `Helium.app`, bundle id `net.imput.helium` |
| Cookies | `<profile>\Network\Cookies` | `<profile>/Cookies` |
| Encryption key | DPAPI, `Local State` → `os_crypt.encrypted_key` | Keychain item **"Helium Storage Key"** (not the usual "<name> Safe Storage"; a separate "Chromium Safe Storage" item belongs to other Chromium apps — never touch it) |
| Device-bound sessions (DBSC) | not present yet | `Device Bound Sessions` file present → those logins are hardware-bound and must never be copied |

Owner's devices (20 Oct 2026): both Helium **0.19.4.1** / Chromium **155.0.8059.84**. Mac: MacBook Air M5, macOS 27.0.1 (26A434), Helium arm64-only build, profiles `Default`="Personal", `Profile 1`="Work". Windows: Windows 11 Pro 26100 x64, one near-empty profile `Default`="Your Helium".

- **Test matrix (CI on every change):** Windows x64 + Windows ARM64, macOS Apple Silicon + macOS Intel, oldest and newest OS versions Helium supports, current and previous Helium release, per-user and machine-wide installs, custom `--user-data-dir`, non-English profile names, paths with spaces and non-ASCII characters.
- **Community compatibility reports:** `scripts/check-*.{sh,ps1}` output (no secrets) lets testers report unusual setups before release.

## 1c. Helium version gate (zero-risk rule)

Owner rule: if there is even a 1% chance that different Helium versions could cause an error, don't sync — tell the user to update first.

- Every device reports its Helium version + Chromium version + profile format versions with each upload.
- Syncing between two devices is allowed only when **both** versions are on the **tested-compatible list** shipped with the app (built from the CI matrix). Same version on both = allowed only if that version itself is tested.
- Otherwise: **apply nothing**, keep local changes queued (nothing is lost), and show a plain message, e.g.
  *"Your Mac has Helium 0.19.5 and this PC has 0.19.4. Update Helium on this PC (Settings → About) to keep syncing. Your changes are saved and will sync after the update."*
- An unknown newer Helium version on any device → pause everywhere for that data type until Helium Syncer itself is updated ("Helium Syncer needs an update for Helium 0.20").
- After an update, the first sync does a dry-run check before applying.

## 1d. Never touch the user's existing data

- **Profiles are added, never merged or deleted.** Joining a device adds the other device's profiles as new profiles next to the local ones. Example: Windows has "Your Helium"; joining adds "Personal" and "Work" → Windows shows all three. Removing "Your Helium" is the user's own choice, done in Helium.
- Local profiles are kept as they are, with their own logins and data. Each gets a "Sync this profile to my other devices" switch on the join screen (on if it has real data, off if it's empty or new).
- Profiles are only linked to each other if the user explicitly says so.
- Deletions only ever travel for items that were synced before (tombstones). Data that was never synced is never removed by Helium Syncer.

## 1e. Owner's real data is off-limits during development

The owner's Mac Helium is fully set up with real logins. Until a phase passes every test:

- Tests only use throwaway profiles started with `--user-data-dir=<test folder>`, never the default folder, on both devices.
- The real profile is only ever **read**, from a copied snapshot, never opened in place.
- The macOS Keychain item "Helium Storage Key" is never written, deleted or re-created. Reading it (phase 3 only) needs the owner's explicit OK.
- Before the first real sync: full backup of the whole user data folder, a test restore of that backup, and a dry-run report the owner approves.
- First real sync direction: **Mac → Windows only** (Windows profile is nearly empty); the Mac is not written to until Windows → Mac passes on test profiles.

## 2. User-facing behaviour

1. **Quit Helium** → helper captures changes, encrypts, uploads (seconds).
2. **Helium closed** on another device → helper downloads and applies changes in the background.
3. **Open Helium** → one last quick check (launcher shortcut), then Helium starts already up to date.
4. **Helium open** → helper never touches its files. Changes from elsewhere arrive at next open.
- macOS: "quit" = Cmd+Q (app process exits), not closing the last window.

First setup:
- Device 1 (already set up): install → "Start a new sync" → Helium Syncer shows the **sync code** (e.g. `K7QM-2XPA-9RTD-…`) to save in a password manager → uploads everything.
- Device 2: install → "Join a sync" → paste the code → local profile backed up → other devices' profiles **added**, never merged or erased.
- The sync code is the only secret: it derives the account id, the join secret (server keeps only its hash) and the encryption key. Lost code + no device left = data can't be decrypted (by design). Anyone with the code can join, so it is shown once with a "save it in your password manager" step; "change sync code" is planned.

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
- **Server** (Cloudflare, owner-hosted at https://helium.aiwithrishabh.com, self-host optional): Worker (TypeScript) + D1 (accounts by derived id, devices, ordered encrypted envelopes) + R2 (encrypted blobs). No email, no passwords. Join attempts rate-limited per IP.
- **Encryption**: key derived from the 160-bit sync code (HKDF-SHA256); XChaCha20-Poly1305 for all payloads, bound to the account id. Server sees only ciphertext + sizes.
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
