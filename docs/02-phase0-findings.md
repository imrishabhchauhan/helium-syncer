# Phase 0 findings

Verified on Windows 11 x64, Helium 0.19.4.1 / Chromium 155.0.8059.84, throwaway profiles only (`test-profiles/`, git-ignored).

## Test instances

- A separate Helium instance starts with `--user-data-dir=<test folder> --remote-debugging-port=<port>`; it is fully independent of the user's own Helium.
- `--load-extension=testing/probe-extension` works on Helium (unbranded Chromium), so tests can drive tabs, tab groups, bookmarks, history and extensions through the probe's service worker over the DevTools protocol.
- `Browser.close` over DevTools shuts the instance down cleanly, so it writes its session files like a normal quit.
- Helium ships uBlock Origin (`blockjmkbacgjkknlgpkjjiijinjdanf`) as a built-in extension (MV2, background page, `location: 5`).

## Protected settings (Secure Preferences)

Source: `services/preferences/tracked/pref_hash_calculator.cc`, `device_id_win.cc`, `device_id_mac.cc` at tag 155.0.8059.84.

- Legacy MAC = `UPPERHEX(HMAC-SHA256(key = seed, device_id ‖ path ‖ value_json))`.
- **Seed is empty** for Helium (all 26 MACs + `super_mac` reproduced with `seed = ""`).
- Device id: Windows = machine SID string from `LookupAccountName(<computer name>)`; macOS = `IOPlatformUUID`.
- `value_json` = `base::WriteJson` after removing empty dicts/lists nested in dicts. Exact rules: sorted keys, no spaces, `<` / U+2028 / U+2029 / control chars escaped as backslash-u + 4 upper-case hex, ints only within int32, doubles in shortest ECMAScript form with `.0` appended when integral.
- `super_mac` = MAC over path `""` and the serialised `protection.macs` dictionary.
- Encrypted hashes (`*_encrypted_hash`, `super_encrypted_hash`) = os_crypt-encrypted `SHA256(seed ‖ path ‖ value_json)`.
- Our serialiser re-creates `Secure Preferences`, `Preferences` and `Local State` **byte for byte**.

### Re-signing experiment (`testing/lab/resign-experiment.ps1`)

| Case | Result after Helium start + clean quit |
|---|---|
| A: signed for a foreign device id | homepage reset, 1 extension removed (the classic "copied profile" failure) |
| B: re-signed for this PC, encrypted hashes **dropped** | change kept, all extensions kept, Helium rebuilt the encrypted hashes itself |
| C: re-signed for this PC, stale encrypted hashes kept | homepage reset |

**Rule:** when writing protected settings on a device, recompute legacy MACs with that device's id and delete every encrypted hash. No access to the OS key store (DPAPI / macOS Keychain) is needed for settings or extensions.

Open: confirm the same on macOS (device id = IOPlatformUUID) with a throwaway profile on the owner's Mac.

## Tooling notes

- Windows PowerShell 5.1 strips embedded double quotes when passing arguments to native programs; lab scripts run under PowerShell 7.
- Some editing tools decode backslash-u escapes in source text; the serialiser builds escapes with `%c` and tests build them with a helper, so no raw escape sequences live in the code.
