package window

import "testing"

func TestSize(t *testing.T) {
	dpi, _, _ := procDpi.Call()
	w, h := size()
	t.Logf("system dpi %d, window %dx%d", dpi, w, h)
	if w < Width*9/10 || h < 500 {
		t.Fatalf("window too small: %dx%d", w, h)
	}
}
