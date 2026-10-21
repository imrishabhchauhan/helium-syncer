package window

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	mu   sync.Mutex
	open webview2.WebView
)

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procShowWindow  = user32.NewProc("ShowWindow")
	procSetFgWindow = user32.NewProc("SetForegroundWindow")
	procDpi         = user32.NewProc("GetDpiForSystem")
	procDpiCtx      = user32.NewProc("SetProcessDpiAwarenessContext")
	procMetrics     = user32.NewProc("GetSystemMetrics")
)

// Sharp text on scaled displays (125 %, 150 %): the process says it knows
// about scaling, and sizes come from the display setting. Older Windows
// versions without these calls keep the default.
func init() {
	if procDpiCtx.Find() == nil {
		procDpiCtx.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	}
}

// size is the window size in pixels for this display, at most 92 % of the screen.
func size() (uint, uint) {
	dpi := uintptr(96)
	if procDpi.Find() == nil {
		if r, _, _ := procDpi.Call(); r > 0 {
			dpi = r
		}
	}
	w, h := uint(Width)*uint(dpi)/96, uint(Height)*uint(dpi)/96
	sw, _, _ := procMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procMetrics.Call(1) // SM_CYSCREEN
	if max := uint(sw) * 92 / 100; sw > 0 && w > max {
		w = max
	}
	if max := uint(sh) * 92 / 100; sh > 0 && h > max {
		h = max
	}
	return w, h
}

// Open shows the window with the page at url, or brings it to the front.
// dataDir keeps the window's own browser data (never Helium's).
func Open(url, dataDir string) error {
	mu.Lock()
	if w := open; w != nil {
		mu.Unlock()
		w.Dispatch(func() {
			h := uintptr(w.Window())
			procShowWindow.Call(h, 9) // SW_RESTORE
			procSetFgWindow.Call(h)
		})
		return nil
	}
	mu.Unlock()
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // the window and its messages live on this thread
		w, h := size()
		wv := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      filepath.Join(dataDir, "window"),
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: "Helium Syncer", Width: w, Height: h, Center: true},
		})
		if wv == nil {
			ready <- errors.New("the WebView2 runtime is not available")
			return
		}
		wv.SetSize(int(w)*720/Width, int(h)*520/Height, webview2.HintMin)
		wv.Navigate(url)
		mu.Lock()
		open = wv
		mu.Unlock()
		ready <- nil
		wv.Run() // until the person closes the window
		mu.Lock()
		open = nil
		mu.Unlock()
		wv.Destroy()
	}()
	if err := <-ready; err != nil {
		// No WebView2: Edge in app mode looks like an app window too, and
		// Edge is part of Windows 10 and 11.
		if e := exec.Command("cmd", "/c", "start", "", "msedge", "--app="+url).Run(); e == nil {
			return nil
		}
		return err
	}
	return nil
}

// Close closes the window, if it is open.
func Close() {
	mu.Lock()
	w := open
	mu.Unlock()
	if w != nil {
		w.Dispatch(w.Terminate)
	}
}
