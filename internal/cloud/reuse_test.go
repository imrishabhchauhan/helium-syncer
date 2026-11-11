package cloud

import (
	"net/http"
	"testing"
	"time"
)

// Go keeps an unused connection for 90 seconds by default. Syncs are five
// minutes apart on battery, so by then the connection is gone and the next
// sync starts with a fresh handshake: more processor, and more time with
// the radio awake, which is what a laptop pays for. The pool must keep it
// for longer than the gap between syncs.
func TestAConnectionIsKeptLongerThanTheGapBetweenSyncs(t *testing.T) {
	const gapOnBattery = 5 * time.Minute
	if shared.IdleConnTimeout <= gapOnBattery {
		t.Errorf("an unused connection is dropped after %v, and syncs are %v apart on battery",
			shared.IdleConnTimeout, gapOnBattery)
	}
}

// The server's name gives several addresses, and Go tries them one after
// another, splitting the wait between them. So half the wait is what one
// hanging address costs before the good one is tried. Go's 30-second
// default made every sync on the owner's PC start with a 13 to 16 second
// pause while a working address sat there untried.
func TestAHangingAddressIsGivenUpOnQuickly(t *testing.T) {
	const addresses = 2 // what the server's name gives today
	perAddress := DialFor / addresses
	if perAddress > 5*time.Second {
		t.Errorf("one hanging address costs %v before the next is tried", perAddress)
	}
	// And still far more than a healthy connection needs, which was
	// measured at 0.15 seconds.
	if perAddress < time.Second {
		t.Errorf("only %v for each address, which a slow link would not manage", perAddress)
	}
}

// A client given its own HTTP client keeps it: tests and the probe rely on
// that, and nothing should quietly reach the shared pool instead.
func TestAnOwnHTTPClientIsKept(t *testing.T) {
	mine := &http.Client{}
	c := &Client{Base: "http://example.invalid", HTTP: mine}
	if c.http() != mine {
		t.Error("the client's own HTTP client was replaced")
	}
}
