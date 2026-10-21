// Helium Syncer app: lives in the Windows tray / macOS menu bar, runs the
// background sync and opens the Helium Syncer window.
//
// Build it as a GUI program (no console window): see scripts/build-app.*.
// Unofficial and not affiliated with Helium or imput.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/agent"
	"github.com/imrishabhchauhan/helium-syncer/internal/app"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/power"
	"github.com/imrishabhchauhan/helium-syncer/internal/tray"
	"github.com/imrishabhchauhan/helium-syncer/internal/ui"
)

func main() {
	// Started at login: stay quiet in the tray. Started by a person: open
	// the window too.
	background := len(os.Args) > 1 && os.Args[1] == "--background"
	agent.AutostartArgs = []string{"--background"}
	power.Efficient() // Windows efficiency mode: low clock speed, efficient cores
	logger := openLog()

	lock, err := agent.Lock()
	if err != nil {
		// Already running: ask that copy to show its window and stop here.
		if agent.AskToShow() == nil {
			return
		}
		// The command-line agent holds the lock. It does the syncing; this
		// copy only shows the window and the tray.
		logger.Printf("the command-line agent is running; showing the window without a second sync")
	}

	s := &ui.Server{}
	if _, err := s.Start(); err != nil {
		logger.Printf("cannot start the window: %v", err)
		os.Exit(1)
	}
	open := func() {
		if os.Getenv("HELIUM_SYNCER_NO_BROWSER") != "" { // tests: never open a real browser
			logger.Printf("open window requested: %s", s.URL())
			return
		}
		if err := agent.OpenBrowser(s.URL()); err != nil {
			logger.Printf("cannot open the window: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if lock != nil {
		go agent.ServeShow(lock, open)
		go keepAgent(ctx, s, logger)
	}
	if !background {
		open()
	}

	tray.Run(tray.Options{
		Status: func() tray.View { return view(s, lock == nil) },
		Open:   open,
		SyncNow: func() {
			if a := s.Agent(); a != nil {
				select {
				case a.Trigger <- struct{}{}:
				default:
				}
			}
		},
		OnQuit: func() {
			// Let a sync that is writing finish first (it is crash-safe
			// anyway, but finishing is kinder).
			if a := s.Agent(); a != nil {
				for i := 0; i < 120 && a.Status.Snapshot().Syncing; i++ {
					time.Sleep(time.Second)
				}
			}
			cancel()
			if lock != nil {
				lock.Close()
			}
		},
	})
}

// keepAgent starts the background sync as soon as this computer is
// connected (right away, or once the person finishes the first steps).
func keepAgent(ctx context.Context, s *ui.Server, logger *log.Logger) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if s.Agent() == nil {
			if cfg, err := app.Load(); err == nil && cfg.Connected() {
				if a, err := agent.New(); err == nil {
					s.SetAgent(a)
					go a.Run(ctx)
					logger.Printf("background sync started")
				} else {
					logger.Printf("cannot start the background sync: %v", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func view(s *ui.Server, elsewhere bool) tray.View {
	cfg, err := app.LoadCached()
	if err != nil {
		return tray.View{LastError: err.Error(), Connected: true, SetupDone: true}
	}
	v := tray.View{Connected: cfg.Connected(), SetupDone: cfg.SetupDone, LastSync: cfg.LastSync, LastError: cfg.LastError, Elsewhere: elsewhere}
	if a := s.Agent(); a != nil {
		st := a.Status.Snapshot()
		v.HeliumOpen, v.Syncing, v.Paused, v.LastError, v.PowerNote = st.HeliumOpen, st.Syncing, st.Paused, st.LastError, st.PowerNote
		if st.LastSync.After(v.LastSync) {
			v.LastSync = st.LastSync
		}
	} else if cfg.UserData != "" {
		v.HeliumOpen, _ = helium.IsRunning(cfg.UserData)
	}
	return v
}

// openLog writes to app.log next to the settings (a GUI program has no
// console to print to).
func openLog() *log.Logger {
	dir := app.Dir()
	if err := os.MkdirAll(dir, 0o700); err == nil {
		p := filepath.Join(dir, "app.log")
		if info, err := os.Stat(p); err == nil && info.Size() > 1<<20 {
			os.Rename(p, p+".1")
		}
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			l := log.New(f, "", log.LstdFlags)
			l.Printf("Helium Syncer %s starting (%s)", app.Version, fmt.Sprint(os.Args[1:]))
			return l
		}
	}
	return log.New(os.Stderr, "", log.LstdFlags)
}
