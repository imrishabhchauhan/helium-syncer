package agent

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func taskFor(t *testing.T) (string, map[string]string) {
	t.Helper()
	x := taskXML(`C:\Programs\Helium Syncer\Helium Syncer.exe`, []string{"--background"}, `PC\rishabh`)
	var doc struct {
		Settings struct {
			DisallowStartIfOnBatteries string `xml:"DisallowStartIfOnBatteries"`
			StopIfGoingOnBatteries     string `xml:"StopIfGoingOnBatteries"`
			ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
			MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
			Hidden                     string `xml:"Hidden"`
			RunOnlyIfNetworkAvailable  string `xml:"RunOnlyIfNetworkAvailable"`
			RestartOnFailure           struct {
				Interval string `xml:"Interval"`
				Count    string `xml:"Count"`
			} `xml:"RestartOnFailure"`
		} `xml:"Settings"`
		Triggers struct {
			Time struct {
				Repetition struct {
					Interval string `xml:"Interval"`
				} `xml:"Repetition"`
			} `xml:"TimeTrigger"`
			Logon struct {
				Enabled string `xml:"Enabled"`
			} `xml:"LogonTrigger"`
		} `xml:"Triggers"`
		Principals struct {
			Principal struct {
				RunLevel  string `xml:"RunLevel"`
				LogonType string `xml:"LogonType"`
			} `xml:"Principal"`
		} `xml:"Principals"`
		Actions struct {
			Exec struct {
				Command   string `xml:"Command"`
				Arguments string `xml:"Arguments"`
			} `xml:"Exec"`
		} `xml:"Actions"`
	}
	// The text says UTF-16 because that is what schtasks reads from the
	// file; here it is still ordinary text, so hand it through as it is.
	d := xml.NewDecoder(strings.NewReader(x))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&doc); err != nil {
		t.Fatalf("the task is not valid XML: %v", err)
	}
	return x, map[string]string{
		"onBatteriesStart": doc.Settings.DisallowStartIfOnBatteries,
		"onBatteriesStop":  doc.Settings.StopIfGoingOnBatteries,
		"timeLimit":        doc.Settings.ExecutionTimeLimit,
		"instances":        doc.Settings.MultipleInstancesPolicy,
		"hidden":           doc.Settings.Hidden,
		"needsNetwork":     doc.Settings.RunOnlyIfNetworkAvailable,
		"restartInterval":  doc.Settings.RestartOnFailure.Interval,
		"restartCount":     doc.Settings.RestartOnFailure.Count,
		"runLevel":         doc.Principals.Principal.RunLevel,
		"logonType":        doc.Principals.Principal.LogonType,
		"repeatEvery":      doc.Triggers.Time.Repetition.Interval,
		"startsAtLogin":    doc.Triggers.Logon.Enabled,
		"command":          doc.Actions.Exec.Command,
		"arguments":        doc.Actions.Exec.Arguments,
	}
}

// The whole point of the task is that it starts the program again if it
// stops. Without that it is no better than the registry entry it replaces.
func TestTheTaskStartsTheProgramAgainIfItStops(t *testing.T) {
	_, got := taskFor(t)
	// Windows's own "restart if it fails" did not fire when the program was
	// killed on the owner's PC: the task recorded the failure (-1) and did
	// nothing. The repeat is what actually brings it back, and because two
	// copies are never allowed it does nothing while the program is there.
	if got["repeatEvery"] != "PT1M" {
		t.Errorf("checks every %q, want every minute", got["repeatEvery"])
	}
	if got["instances"] != "IgnoreNew" {
		t.Errorf("instances is %q, so the repeat would start a second copy every five minutes", got["instances"])
	}
	if got["startsAtLogin"] != "true" {
		t.Error("it would not start when the person logs in")
	}
	if got["restartInterval"] != "PT1M" {
		t.Errorf("waits %q before starting it again, want one minute", got["restartInterval"])
	}
}

// Windows stops a task when a laptop is unplugged, unless told not to.
// That would stop the sync at exactly the moment the person leaves their
// desk, which is the opposite of a sync that is careful with the battery.
func TestBatteryDoesNotStopTheTask(t *testing.T) {
	_, got := taskFor(t)
	if got["onBatteriesStop"] != "false" {
		t.Errorf("Windows would stop the sync when the laptop is unplugged (StopIfGoingOnBatteries=%q)", got["onBatteriesStop"])
	}
	if got["onBatteriesStart"] != "false" {
		t.Errorf("Windows would not start the sync on battery (DisallowStartIfOnBatteries=%q)", got["onBatteriesStart"])
	}
}

// It runs all day, needs no administrator, shows no window, and never runs
// twice at once.
func TestTheTaskIsQuietAndOrdinary(t *testing.T) {
	_, got := taskFor(t)
	for _, c := range []struct{ key, want, why string }{
		{"timeLimit", "PT0S", "Windows would stop it after three days"},
		{"instances", "IgnoreNew", "a second copy could be started"},
		{"hidden", "true", "it would show in the task list as something to worry about"},
		{"runLevel", "LeastPrivilege", "it would ask for administrator rights"},
		{"logonType", "InteractiveToken", "it would not reach the person's own desktop or key store"},
		{"needsNetwork", "false", "it would not start at all when the network is slow to come up"},
	} {
		if got[c.key] != c.want {
			t.Errorf("%s is %q, want %q: otherwise %s", c.key, got[c.key], c.want, c.why)
		}
	}
}

// The task must name the window program, not the command-line one, or a
// black terminal window sits on the screen all day.
func TestTheTaskStartsTheWindowProgram(t *testing.T) {
	_, got := taskFor(t)
	if !strings.HasSuffix(got["command"], appExe) {
		t.Errorf("the task starts %q, want the window program", got["command"])
	}
	if got["arguments"] != "--background" {
		t.Errorf("arguments %q, want --background", got["arguments"])
	}
}

// schtasks reads the file as UTF-16 and refuses anything else.
func TestTheTaskFileIsWhatWindowsReads(t *testing.T) {
	b := utf16BOM("<Task/>")
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatal("the file has no byte order mark, so schtasks refuses it")
	}
	if b[2] != '<' || b[3] != 0 {
		t.Errorf("the text is not two bytes per letter: %v", b[2:6])
	}
}
