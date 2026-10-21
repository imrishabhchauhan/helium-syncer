// Package tray shows Helium Syncer in the Windows tray / macOS menu bar:
// a status line, "Open Helium Syncer", "Sync now" and "Quit".
package tray

import (
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
)

// Options connect the tray to the app.
type Options struct {
	Status  func() View // asked every 5 seconds; keep it cheap
	Open    func()      // show the Helium Syncer window
	SyncNow func()
	OnQuit  func() // runs before the app exits
}

// Run shows the tray and blocks until Quit. Call it from main: macOS needs
// the menu bar on the main thread.
func Run(o Options) {
	systray.Run(func() { ready(o) }, o.OnQuit)
}

var (
	iconsOnce sync.Once
	icoBlue   []byte
	icoAmber  []byte
	pngBlue   []byte
	pngAmber  []byte
	pngMark   []byte
)

func icons() {
	iconsOnce.Do(func() {
		icoBlue = icoBytes(mark(16, blue, white, true), mark(32, blue, white, true), mark(48, blue, white, true))
		icoAmber = icoBytes(mark(16, amber, white, true), mark(32, amber, white, true), mark(48, amber, white, true))
		pngBlue = pngBytes(mark(64, blue, white, true))
		pngAmber = pngBytes(mark(32, amber, white, true))
		pngMark = pngBytes(mark(32, black, black, false))
	})
}

func setIcon(attention bool) {
	icons()
	switch runtime.GOOS {
	case "windows":
		if attention {
			systray.SetIcon(icoAmber)
		} else {
			systray.SetIcon(icoBlue)
		}
	case "darwin":
		if attention {
			systray.SetIcon(pngAmber) // a coloured icon stands out in the menu bar
		} else {
			systray.SetTemplateIcon(pngMark, pngMark) // tinted by macOS for light/dark
		}
	default:
		if attention {
			systray.SetIcon(pngAmber)
		} else {
			systray.SetIcon(pngBlue)
		}
	}
}

func ready(o Options) {
	systray.SetTooltip("Helium Syncer")
	status := systray.AddMenuItem("Helium Syncer", "")
	status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Open Helium Syncer", "Status, computers and settings")
	syncNow := systray.AddMenuItem("Sync now", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit Helium Syncer", "Stops syncing until you open Helium Syncer again")
	if runtime.GOOS == "windows" {
		// Windows habit: left click opens the app, right click the menu.
		systray.SetOnTapped(o.Open)
	}

	lastText, lastAttention, first := "", false, true
	refresh := func() {
		v := o.Status()
		text, attention := Line(v, time.Now())
		if text != lastText {
			status.SetTitle(text)
			systray.SetTooltip("Helium Syncer · " + text)
			lastText = text
		}
		if attention != lastAttention || first {
			setIcon(attention)
			lastAttention, first = attention, false
		}
		switch {
		case !v.Connected || !v.SetupDone || v.Elsewhere:
			syncNow.SetTitle("Sync now")
			syncNow.Disable()
		case v.HeliumOpen:
			syncNow.SetTitle("Sync now · quit Helium first")
			syncNow.Disable()
		case v.Syncing:
			syncNow.SetTitle("Syncing…")
			syncNow.Disable()
		default:
			syncNow.SetTitle("Sync now")
			syncNow.Enable()
		}
	}
	refresh()
	go func() {
		tick := time.NewTicker(5 * time.Second) // only reads memory and one file date
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				refresh()
			case <-open.ClickedCh:
				o.Open()
			case <-syncNow.ClickedCh:
				o.SyncNow()
				refresh()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
