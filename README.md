# Helium Syncer

**Sync your Helium browser between Mac and Windows: profiles, tabs, tab groups, bookmarks, history, settings and extensions.**

> [!IMPORTANT]
> **Helium Syncer is NOT affiliated with, endorsed by or sponsored by the official [Helium](https://helium.computer) project or imput.**
> It is an independent hobby project, built to help the community and fellow Helium lovers get a good multi-device experience.
> "Helium" is the name of their browser; it is used here only to say what this tool works with.

> [!WARNING]
> **Early development. Not ready for daily use yet.** Nothing here touches your real browser profile until the test suite passes. Watch or star the repo to follow progress.

---

## A short note from me

I love the official Helium browser. It's fast, minimal and private, and it's the browser I want to use every day.

It doesn't have sync yet, and I work on a Mac and a Windows PC, so for my work that was a must. So I built one with [Claude Code](https://claude.com/claude-code) 🙂

Once the official Helium team releases sync support ([imputnet/helium#90](https://github.com/imputnet/helium/issues/90)), this project will be archived. Until then, enjoy.

— [Rishabh Chauhan](https://aiwithrishabh.com)

---

## The goal: easier, faster, more reliable

- **Easy:** install, type your email code, done. No servers, keys, git or config files to deal with. Your second device sets itself up: profiles, extensions, tabs and all.
- **Fast:** changes upload a few seconds after you quit Helium, and the other device applies them in the background, so Helium opens already up to date.
- **Reliable:** a backup before every change, item-by-item merging (nothing gets blindly overwritten), and a hard stop instead of a guess if a Helium update changes a file format.

## How it works

1. **You quit Helium** on one device → Helium Syncer saves what changed, encrypts it and uploads it.
2. **Helium is closed** on your other device → Helium Syncer downloads the changes and applies them.
3. **You open Helium** there → everything is already in place.
4. **While Helium is open**, Helium Syncer never touches its files. Your tabs never change under you.

On macOS, "quit" means **Cmd+Q**, not just closing the window.

## What syncs

| Data | How it merges |
|---|---|
| Profiles (name, colour, avatar) | Missing profiles are created automatically |
| Bookmarks | Item-by-item; deletions carry over |
| History | Both devices combined; deletions carry over |
| Open tabs, pinned tabs, tab groups | Combined, no duplicates; closed tabs stay closed |
| Settings | Synced, except device-only ones (download folder, window size) |
| Extensions + their settings | Installed on the other device with their data (e.g. uBlock rules) |
| Logins (cookies) | **Off by default.** Opt in per site. Google, Microsoft, Apple and banks stay locked unless you unlock them |
| Passwords | Not synced. Use a password manager such as 1Password or Bitwarden |

## Privacy

- **End-to-end encrypted.** Data is encrypted on your device before upload. The server only stores scrambled data it can't read.
- **Sign in with an email code.** New devices are approved from a device you already have. A recovery key covers the case where no old device is left.
- **Self-host option.** Run the Cloudflare server on your own account if you prefer.
- **Free and open source** under the MIT license.

## Roadmap

- [ ] **Phase 0:** read and write every Helium data type safely on test profiles (Windows + macOS)
- [ ] **Phase 1:** accounts, encryption, profiles, bookmarks, history, tabs, pinned tabs, tab groups, settings
- [ ] **Phase 2:** extensions and extension data
- [ ] **Phase 3:** opt-in logins per site
- [ ] **Phase 4:** signed installers, auto-update, self-host guide

The full design is in [docs/01-spec.md](docs/01-spec.md).

## Help with testing

Got Helium on a Mac or Windows PC? The read-only check scripts in [`scripts/`](scripts/) report where Helium keeps its data on your machine. They never print secrets or change anything.

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/imrishabhchauhan/helium-syncer/main/scripts/check-mac.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/imrishabhchauhan/helium-syncer/main/scripts/check-windows.ps1 | iex
```

## License

[MIT](LICENSE) © 2026 Rishabh Chauhan
