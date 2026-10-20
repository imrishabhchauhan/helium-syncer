// hslab is the phase-0 lab tool: small experiments against throwaway Helium
// test profiles. It is not the product and never touches a real profile
// unless a path is given explicitly, and even then only reads.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/cdp"
	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: hslab macs <profile-dir> | close <port>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "macs":
		err = macs(os.Args[2])
	case "resign":
		err = resign(os.Args[2], os.Args[3:])
	case "open":
		port, _ := strconv.Atoi(os.Args[2])
		err = openURL(port, os.Args[3])
	case "eval": // eval <port> <url-substring> <js>
		port, _ := strconv.Atoi(os.Args[2])
		err = evalIn(port, os.Args[3], os.Args[4])
	case "schema":
		err = schema(os.Args[2])
	case "roundtrip":
		err = roundtrip(os.Args[2])
	case "close":
		port, _ := strconv.Atoi(os.Args[2])
		err = closeBrowser(port)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// macs recomputes every legacy MAC in a profile's Preferences and Secure
// Preferences and reports which ones match what Helium wrote.
func macs(profile string) error {
	dev, err := deviceid.Get()
	if err != nil {
		return err
	}
	calc := prefhash.Calculator{DeviceID: dev}
	fmt.Printf("device id: %s…%s (len %d)\n", dev[:6], dev[len(dev)-4:], len(dev))
	for _, file := range []string{"Secure Preferences", "Preferences"} {
		data, err := os.ReadFile(filepath.Join(profile, file))
		if err != nil {
			return err
		}
		doc, err := chromejson.Decode(data)
		if err != nil {
			return err
		}
		macsTree, _ := chromejson.Lookup(doc, "protection.macs").(map[string]any)
		if macsTree == nil {
			fmt.Printf("\n[%s] no protection.macs\n", file)
			continue
		}
		var ok, bad []string
		walk(macsTree, "", func(path, stored string) {
			if strings.HasSuffix(path, "_encrypted_hash") || strings.Contains(path, "_encrypted_hash.") {
				return
			}
			got := calc.MAC(path, prefhash.ValueString(chromejson.Lookup(doc, path)))
			if got == stored {
				ok = append(ok, path)
			} else {
				bad = append(bad, path)
			}
		})
		sort.Strings(bad)
		fmt.Printf("\n[%s] legacy MACs: %d match, %d differ\n", file, len(ok), len(bad))
		for _, p := range bad {
			fmt.Println("  differs:", p)
		}
		if super, _ := chromejson.Lookup(doc, "protection.super_mac").(string); super != "" {
			got := calc.MAC("", prefhash.ValueString(macsTree))
			fmt.Printf("[%s] super_mac match: %v\n", file, got == super)
		}
	}
	return nil
}

// resign rewrites a TEST profile's Secure Preferences: optional
// path=<json> assignments, then all legacy MACs recomputed for this machine.
// Flags: --keep-encrypted keeps the encrypted hashes (default drops them),
// --device=<id> signs for a different device id (to simulate a foreign PC).
func resign(profile string, args []string) error {
	if !strings.Contains(filepath.ToSlash(profile), "/test-profiles/") {
		return fmt.Errorf("refusing: %s is not under test-profiles/", profile)
	}
	file := filepath.Join(profile, "Secure Preferences")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	v, err := chromejson.Decode(data)
	if err != nil {
		return err
	}
	doc := v.(map[string]any)
	dev, err := deviceid.Get()
	if err != nil {
		return err
	}
	drop := true
	var extra []string
	for _, a := range args {
		switch {
		case a == "--keep-encrypted":
			drop = false
		case strings.HasPrefix(a, "--device="):
			dev = strings.TrimPrefix(a, "--device=")
		default:
			path, raw, ok := strings.Cut(a, "=")
			if !ok {
				return fmt.Errorf("bad assignment %q", a)
			}
			val, err := chromejson.Decode([]byte(raw))
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			chromejson.Set(doc, path, val)
			extra = append(extra, path)
		}
	}
	n := prefhash.Resign(doc, prefhash.Calculator{DeviceID: dev}, drop, extra...)
	tmp := file + ".hs-tmp"
	if err := os.WriteFile(tmp, []byte(chromejson.Compact(doc)), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	fmt.Printf("re-signed %d MACs (encrypted hashes dropped: %v)\n", n, drop)
	return nil
}

// schema prints the meta table and CREATE statements of a SQLite database,
// reading a temporary copy so the original is never opened.
func schema(file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	tmp := filepath.Join(os.TempDir(), "hslab-schema.db")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(tmp)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	if rows, err := db.Query("SELECT key, value FROM meta ORDER BY key"); err == nil {
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			fmt.Printf("meta %s = %s\n", k, v)
		}
		rows.Close()
	}
	rows, err := db.Query("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type DESC, name")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, stmt string
		rows.Scan(&typ, &name, &stmt)
		fmt.Printf("\n-- %s %s\n%s\n", typ, name, stmt)
	}
	return rows.Err()
}

// roundtrip checks that our serialiser reproduces a Chromium-written JSON
// file byte for byte, and shows the first difference if not.
func roundtrip(file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	doc, err := chromejson.Decode(data)
	if err != nil {
		return err
	}
	got := chromejson.Compact(doc)
	want := string(data)
	if got == want {
		fmt.Printf("roundtrip OK: %d bytes identical\n", len(want))
		return nil
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := max(0, i-80)
	fmt.Printf("roundtrip DIFFERS at byte %d (ours %d bytes, file %d bytes)\n", i, len(got), len(want))
	fmt.Printf("file: …%q\n", want[lo:min(len(want), i+60)])
	fmt.Printf("ours: …%q\n", got[lo:min(len(got), i+60)])
	return nil
}

func walk(node map[string]any, prefix string, fn func(path, mac string)) {
	for k, v := range node {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		switch t := v.(type) {
		case string:
			fn(p, t)
		case map[string]any:
			walk(t, p, fn)
		}
	}
}

// evalIn evaluates JavaScript in the first target whose URL contains sub.
func evalIn(port int, sub, js string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts, err := cdp.Targets(ctx, port)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if strings.Contains(t.URL, sub) {
			c, err := cdp.Dial(ctx, t.WSURL)
			if err != nil {
				return err
			}
			defer c.Close()
			var out any
			if err := c.Eval(ctx, js, &out); err != nil {
				return err
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
			return nil
		}
	}
	return fmt.Errorf("no target with URL containing %q", sub)
}

// openURL opens a new tab in a test instance via Target.createTarget.
func openURL(port int, url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ws, err := cdp.BrowserWSURL(ctx, port)
	if err != nil {
		return err
	}
	c, err := cdp.Dial(ctx, ws)
	if err != nil {
		return err
	}
	defer c.Close()
	// No Browser.setDownloadBehavior here: it makes Helium refuse extension
	// downloads. Test profiles get their own download folder instead
	// (testkit.SeedDownloadDir).
	res, err := c.Call(ctx, "Target.createTarget", map[string]any{"url": url})
	fmt.Println(string(res))
	return err
}

// closeBrowser asks a test instance to shut down cleanly (so it writes its
// session files), via Browser.close.
func closeBrowser(port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ws, err := cdp.BrowserWSURL(ctx, port)
	if err != nil {
		return err
	}
	c, err := cdp.Dial(ctx, ws)
	if err != nil {
		return err
	}
	_, err = c.Call(ctx, "Browser.close", map[string]any{})
	c.Close()
	if err != nil && !strings.Contains(err.Error(), "closed") {
		return err
	}
	fmt.Println("closed test instance on port", port)
	return nil
}
