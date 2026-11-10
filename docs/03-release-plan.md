# Release plan: from "build it yourself" to "download and use" (10 Nov 2026)

**Goal:** a person finds the repository, downloads one file for their computer, follows the setup window, and it keeps working, also after Helium updates. No programming tools, no confusing warnings.

**Today (honest):** it works well between the owner's Mac and Windows PC, but a stranger must build it from source, gets security warnings on both systems, and loses sync at each Helium update until a new Helium Syncer comes out.

---

## The 8 parts, in order

| # | Part | Why it matters | Cost |
|---|---|---|---|
| 1 | **Tests on GitHub for every change** | Nothing broken reaches users | Free (public repositories get GitHub Actions for free) |
| 2 | **Helium updates do not stop sync** | Helium updates often; today each update pauses sync for everyone | Free |
| 3 | **Downloads on the Releases page** | People need a file, not source code | Free |
| 4 | **Signed downloads** | Without signing, macOS blocks the app and Windows shows a warning | Mac: USD 99 a year. Windows: free (SignPath) |
| 5 | **Automatic updates** | Users never need to come back to GitHub | Free |
| 6 | **Server ready for more people** | One shared free server for everyone can run out | Free at first; USD 5 a month later |
| 7 | **Clean leave and uninstall** | "Leave" does not remove the helper extension or Developer mode today | Free |
| 8 | **Clear first-run and help** | If people are confused, nobody uses it | Free |

---

## 1. Tests on GitHub for every change

- A workflow runs `go vet` and all tests on Linux for every push and pull request.
- Windows and macOS tests run on pull requests to `main` and on release tags.
- The real-Helium tests (`-tags helium`) and the server tests (`-tags server`) run on a schedule and before each release.

## 2. Helium updates do not stop sync

Today `internal/helium/version.go` lists one tested Helium version. Any other version pauses sync.

**Recommended:**

- Decide by **file format**, not by version number. Helium Syncer already reads the format versions of each file (Bookmarks, sessions, preferences). A new Helium with the same formats syncs. A new format pauses only that one item (for example tabs), not everything.
- Live sync through the helper extension uses Helium's own extension API, which does not change between Helium versions. It keeps working.
- A scheduled GitHub workflow (once a day) downloads the newest Helium, runs the real-Helium tests, and, if they pass, adds that version to a small "tested" list in the repository. The app reads that list once a day. A new Helium version is then supported without a new app version.

## 3. Downloads on the Releases page

- A git tag (for example `v0.2.0`) starts a workflow that builds:
  - Windows: `Helium-Syncer-Windows-x64.zip` and `-arm64.zip` (later an installer).
  - Mac: `Helium-Syncer-Mac.dmg` (one app for Apple silicon and Intel).
- Each file gets a SHA-256 checksum on the Releases page.
- The README "Set it up" section then starts with **Download**.

## 4. Signed downloads

| | Option | Cost | What the user sees |
|---|---|---|---|
| **Mac** | Apple Developer Program, Developer ID signing and notarization | USD 99 a year | Opens normally. **Recommended before a public release.** |
| Mac | Not signed | Free | macOS blocks it; the user must go to System Settings → Privacy & Security → Open Anyway. Many people stop here. |
| **Windows** | **SignPath Foundation** (free code signing for open-source projects; the signer shown is "SignPath Foundation") | Free | Opens normally after the reputation builds. **Recommended.** |
| Windows | Azure Artifact Signing | USD 9.99 a month | For individuals only in the USA and Canada, so not for the owner (India) as an individual. |
| Windows | Not signed | Free | "Windows protected your PC" → More info → Run anyway. |

## 5. Automatic updates

- Once a day, the app asks GitHub for the newest release (one small request).
- It downloads the file for its system, checks the checksum and the signature, and replaces itself while Helium is closed.
- The helper extension updates with the app (this already works).
- A switch in Settings turns automatic updates off.

## 6. Server ready for more people

Today everyone uses the owner's free Cloudflare server.

- **Free plan:** 100,000 requests a day for all users together. A user plugged in all day makes a few hundred to a few thousand requests a day, so the free plan carries roughly 50 to 200 active users.
- **Paid plan (USD 5 a month):** 10 million requests a month included; enough for thousands of users.
- **Before release:** a per-account daily limit, so one broken install cannot use up everyone's allowance; write `last_seen` at most once an hour (today each request writes it); a self-hosting guide for people who want their own server.

## 7. Clean leave and uninstall

- **Leave** also removes the helper extension from each synced profile and switches Developer mode back off if no other unpacked extension needs it (after the next Helium quit, because Helium must be closed).
- A Windows uninstaller and a Mac "Remove Helium Syncer" menu item do the same and remove the background task.

## 8. Clear first-run and help

- The setup window explains the helper extension and Developer mode in one short screen, before it adds them.
- Fresh screenshots for the README (live tabs, the helper extension).
- A troubleshooting page: "sync paused after a Helium update", "Developer mode is on", "a tab I did not open appeared".
- GitHub issue templates that ask for the output of `scripts/check-*.sh`/`.ps1` (no secrets).
- `SECURITY.md`: how to report a security problem privately.

---

## Suggested order

1. Parts 1 and 2 (no cost, biggest effect on reliability).
2. Parts 3, 5 and 7, then a **test release** for a few people.
3. Part 4 (decide on the Apple fee), parts 6 and 8, then the public release.

Prices checked on 10 Nov 2026: [Apple Developer Program](https://developer.apple.com/support/enrollment/), [Azure Artifact Signing](https://azure.microsoft.com/products/artifact-signing), [SignPath Foundation](https://signpath.org/), [Cloudflare Workers pricing](https://developers.cloudflare.com/workers/platform/pricing/).
