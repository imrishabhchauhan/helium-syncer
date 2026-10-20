// helium-syncer is the command-line version of Helium Syncer.
//
// Unofficial and not affiliated with Helium or imput.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/app"
	"github.com/imrishabhchauhan/helium-syncer/internal/cloud"
	"github.com/imrishabhchauhan/helium-syncer/internal/detect"
	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

const usage = `Helium Syncer %s — sync Helium between your computers (unofficial, not affiliated with Helium/imput)

Getting started
  helium-syncer start               start a new sync on this computer and show its sync code
  helium-syncer join <sync code>    join a sync from another computer
  helium-syncer link <profile>      choose a local profile to sync (folder or name)

Everyday
  helium-syncer sync                sync now (Helium must be closed)
  helium-syncer sync --dry-run      show what a sync would change, change nothing
  helium-syncer status              what is connected and when it last synced

More
  helium-syncer detect              find Helium on this computer
  helium-syncer profiles            list profiles and which ones sync
  helium-syncer unlink <profile>    stop syncing a profile (its data stays)
  helium-syncer devices             computers in this sync
  helium-syncer remove <device id>  disconnect another computer
  helium-syncer code                show the sync code (to add a computer)
  helium-syncer disconnect          leave the sync on this computer

Options: --helium-folder <path>  --name <device name>  --restore-tabs  --server <url>
`

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Printf(usage, app.Version)
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	folder := fs.String("helium-folder", "", "Helium user data folder")
	name := fs.String("name", "", "name for this computer")
	server := fs.String("server", "", "sync server (self-hosting)")
	dry := fs.Bool("dry-run", false, "show what would change, change nothing")
	restore := fs.Bool("restore-tabs", false, "turn on Helium's 'Continue where you left off' so synced tabs open")
	// Allow flags after positional arguments.
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if err := run(cmd, pos, *folder, *name, *server, *dry, *restore); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		os.Exit(1)
	}
}

func run(cmd string, pos []string, folder, name, server string, dry, restore bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cfg, err := app.Load()
	if err != nil {
		return err
	}
	if server != "" {
		cfg.Server = strings.TrimRight(server, "/")
	}
	if name != "" {
		cfg.DeviceName = name
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = app.DefaultDeviceName()
	}
	if folder != "" {
		in, ok := detect.Inspect(folder)
		if !ok {
			return fmt.Errorf("%s is not a Helium folder (no \"Local State\" inside)", folder)
		}
		cfg.UserData = in.UserData
	}
	if restore {
		cfg.RestoreTabs = true
	}
	if cfg.UserData == "" {
		if found := detect.Find(); len(found) > 0 {
			cfg.UserData = found[0].UserData
		}
	}

	switch cmd {
	case "version":
		fmt.Println("Helium Syncer", app.Version)
	case "detect":
		found := detect.Find()
		if len(found) == 0 {
			fmt.Println("Helium was not found in the usual places. Use --helium-folder <path>.")
			return nil
		}
		for _, in := range found {
			fmt.Printf("Helium folder: %s\n  Chromium %s, %d profile(s):\n", in.UserData, in.Version, len(in.Profiles))
			for _, p := range in.Profiles {
				fmt.Printf("    %-12s %s\n", p.Dir, p.Name)
			}
		}
	case "start", "join":
		if cfg.Connected() {
			return errors.New("this computer is already in a sync; run 'helium-syncer disconnect' first to switch")
		}
		code := ""
		if cmd == "start" {
			if code, err = vault.NewSyncCode(); err != nil {
				return err
			}
		} else {
			if len(pos) == 0 {
				return errors.New("paste your sync code: helium-syncer join XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX")
			}
			code = strings.Join(pos, "-")
		}
		r, err := cfg.Connect(ctx, code, cmd == "start")
		if err != nil {
			return err
		}
		if cmd == "start" {
			fmt.Printf("✓ New sync started on %q.\n\nYour sync code:\n\n    %s\n\n", cfg.DeviceName, code)
			fmt.Println("Save it in your password manager now. Enter it on your other computers to join.")
			fmt.Println("Anyone with this code can join your sync, so keep it private.")
		} else {
			fmt.Printf("✓ %q joined the sync (created=%v).\n", cfg.DeviceName, r.Created)
		}
		fmt.Println("\nNext: choose which profiles to sync with 'helium-syncer profiles' and 'helium-syncer link <profile>'.")
		fmt.Println("Profiles from your other computers are added automatically on the first sync.")
	case "profiles":
		return listProfiles(cfg)
	case "link", "unlink":
		if !cfg.Connected() {
			return app.ErrNotConnected
		}
		if len(pos) == 0 {
			return fmt.Errorf("which profile? e.g. helium-syncer %s Default", cmd)
		}
		dir, err := resolveProfile(cfg, strings.Join(pos, " "))
		if err != nil {
			return err
		}
		if cmd == "link" {
			if _, err := engine.LinkProfile(cfg.StateDir(), cfg.DeviceID, dir); err != nil {
				return err
			}
			fmt.Printf("✓ %s will sync. Run 'helium-syncer sync --dry-run' to preview.\n", dir)
		} else {
			if err := engine.UnlinkProfile(cfg.StateDir(), cfg.DeviceID, dir); err != nil {
				return err
			}
			fmt.Printf("✓ %s will no longer sync. Its data stays as it is.\n", dir)
		}
	case "sync":
		ec, err := cfg.EngineConfig()
		if err != nil {
			return err
		}
		if dry {
			rep, err := engine.DryRun(ctx, ec)
			if err != nil {
				return friendly(err)
			}
			printDry(rep)
			return cfg.Save()
		}
		rep, err := engine.Run(ctx, ec)
		if err != nil {
			cfg.LastError = err.Error()
			cfg.Save()
			return friendly(err)
		}
		if rep.Paused != "" {
			fmt.Println("⏸ Sync paused:", rep.Paused)
			return nil
		}
		cfg.LastSync, cfg.LastError = time.Now(), ""
		printReport(rep)
		return cfg.Save()
	case "status":
		return status(ctx, cfg)
	case "devices":
		cl, err := cfg.Client()
		if err != nil {
			return err
		}
		devs, me, err := cl.Devices(ctx)
		if err != nil {
			return err
		}
		for _, d := range devs {
			mark := " "
			if d.ID == me {
				mark = "*"
			}
			fmt.Printf("%s %-36s %-30s %-8s Helium %s, last seen %s\n", mark, d.ID, d.Name, d.Platform, orDash(d.HeliumVersion),
				time.UnixMilli(d.LastSeen).Format("2 Jan 15:04"))
		}
	case "remove":
		cl, err := cfg.Client()
		if err != nil {
			return err
		}
		if len(pos) == 0 {
			return errors.New("which device? see 'helium-syncer devices'")
		}
		if err := cl.Remove(ctx, pos[0]); err != nil {
			return err
		}
		fmt.Println("✓ Removed. That computer can rejoin only with the sync code.")
	case "code":
		code, err := app.Code()
		if err != nil {
			return err
		}
		fmt.Printf("Your sync code:\n\n    %s\n\nAnyone with it can join your sync.\n", code)
	case "disconnect":
		if err := cfg.Disconnect(); err != nil {
			return err
		}
		fmt.Println("✓ This computer left the sync. Helium's data is untouched.")
	default:
		return fmt.Errorf("unknown command %q (see 'helium-syncer help')", cmd)
	}
	return cfg.Save()
}

func friendly(err error) error {
	switch {
	case errors.Is(err, engine.ErrHeliumRunning):
		return errors.New("Helium is open. Quit Helium (Cmd+Q on Mac) and run the sync again")
	case errors.Is(err, vault.ErrDecrypt):
		return errors.New("the synced data could not be decrypted with this sync code")
	}
	var ae *cloud.APIError
	if errors.As(err, &ae) {
		return errors.New(ae.Message)
	}
	return err
}

func resolveProfile(cfg *app.Config, want string) (string, error) {
	list, err := profiles.List(cfg.UserData)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if strings.EqualFold(p.Dir, want) || strings.EqualFold(p.Name, want) {
			return p.Dir, nil
		}
	}
	var names []string
	for _, p := range list {
		names = append(names, fmt.Sprintf("%s (%s)", p.Name, p.Dir))
	}
	return "", fmt.Errorf("no profile %q here; profiles: %s", want, strings.Join(names, ", "))
}

func listProfiles(cfg *app.Config) error {
	if cfg.UserData == "" {
		return errors.New("Helium was not found; use --helium-folder <path>")
	}
	list, err := profiles.List(cfg.UserData)
	if err != nil {
		return err
	}
	links := map[string]string{}
	if cfg.Connected() {
		links, _ = engine.Links(cfg.StateDir(), cfg.DeviceID)
	}
	fmt.Println("Helium folder:", cfg.UserData)
	for _, p := range list {
		state := "not synced"
		if _, ok := links[p.Dir]; ok {
			state = "syncs"
		}
		fmt.Printf("  %-12s %-24s %s\n", p.Dir, p.Name, state)
	}
	return nil
}

func status(ctx context.Context, cfg *app.Config) error {
	fmt.Println("Helium Syncer", app.Version)
	fmt.Println("Helium folder:", orDash(cfg.UserData))
	if !cfg.Connected() {
		fmt.Println("Not connected. Run 'helium-syncer start' or 'helium-syncer join <sync code>'.")
		return nil
	}
	fmt.Printf("This computer: %s\nServer: %s\n", cfg.DeviceName, cfg.Server)
	if cfg.LastSync.IsZero() {
		fmt.Println("Last sync: never")
	} else {
		fmt.Println("Last sync:", cfg.LastSync.Format("Mon 2 Jan 15:04"))
	}
	if cfg.LastError != "" {
		fmt.Println("Last problem:", cfg.LastError)
	}
	if n, _ := engine.Pending(cfg.StateDir(), cfg.DeviceID); n > 0 {
		fmt.Printf("Waiting to send: %d change(s)\n", n)
	}
	if cl, err := cfg.Client(); err == nil {
		if devs, _, err := cl.Devices(ctx); err == nil {
			fmt.Printf("Computers in this sync: %d\n", len(devs))
		}
	}
	return listProfiles(cfg)
}

func printReport(rep engine.Report) {
	fmt.Printf("✓ Synced. Sent %d change(s)%s, received %d%s.\n", rep.Sent, byType(rep.SentByType), rep.Received, byType(rep.ReceivedByType))
	for _, p := range rep.ProfilesAdded {
		fmt.Println("  + new profile from another computer:", p)
	}
	if len(rep.Written) == 0 {
		fmt.Println("  No Helium files needed to change on this computer.")
	} else {
		fmt.Println("  Updated:", strings.Join(rep.Written, ", "))
	}
}

func printDry(rep engine.DryRunReport) {
	fmt.Println("Dry run: nothing was changed and nothing was sent.")
	fmt.Printf("  Would send %d change(s)%s and receive %d%s.\n", rep.Sent, byType(rep.SentByType), rep.Received, byType(rep.ReceivedByType))
	for _, p := range rep.ProfilesAdded {
		fmt.Println("  Would add profile:", p)
	}
	if len(rep.WouldChange) == 0 && len(rep.WouldCreate) == 0 {
		fmt.Println("  No Helium file on this computer would change.")
	}
	for _, f := range rep.WouldChange {
		fmt.Println("  Would update:", f)
	}
	for _, f := range rep.WouldCreate {
		fmt.Println("  Would create:", f)
	}
}

func byType(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	labels := map[string]string{"bookmark": "bookmarks", "visit": "visits", "title": "page titles", "tab": "tabs", "group": "tab groups",
		"setting": "settings", "profile": "profiles", "ext": "extensions", "ext-data": "extension data"}
	var parts []string
	for k, v := range m {
		l := labels[k]
		if l == "" {
			l = k
		}
		parts = append(parts, fmt.Sprintf("%d %s", v, l))
	}
	sort.Strings(parts)
	return " (" + strings.Join(parts, ", ") + ")"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
