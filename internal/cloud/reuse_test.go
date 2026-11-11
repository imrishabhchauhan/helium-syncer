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

// Waiting for a connection must stay well above the sixteen seconds the
// system itself spends retrying a lost one, or a sync that would have
// worked is given up on and has to wait for the next try.
func TestItWaitsLongEnoughForTheSystemToRetry(t *testing.T) {
	const systemRetries = 16 * time.Second
	if DialFor <= systemRetries {
		t.Errorf("gives up after %v, but the system retries for about %v", DialFor, systemRetries)
	}
	if DialFor >= 60*time.Second {
		t.Errorf("waits %v, which is longer than a whole sync is allowed to take", DialFor)
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
