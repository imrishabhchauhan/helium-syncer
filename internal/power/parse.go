package power

import (
	"regexp"
	"strconv"
	"strings"
)

// The parsers have no build tags, so every test runs on every system.

// systemPowerStatus is Windows' SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func parseWindows(st systemPowerStatus) State {
	s := State{Percent: -1}
	s.HasBattery = st.BatteryFlag != 128 && st.BatteryFlag != 255 // 128: no battery, 255: unknown
	s.OnBattery = s.HasBattery && st.ACLineStatus == 0
	s.Saver = st.SystemStatusFlag == 1
	if s.HasBattery && st.BatteryLifePercent <= 100 {
		s.Percent = int(st.BatteryLifePercent)
	}
	return s
}

var percentRe = regexp.MustCompile(`(\d{1,3})%`)
var lowPowerRe = regexp.MustCompile(`(?m)^\s*(lowpowermode|powermode)\s+(\d+)`)

// parsePmset reads "pmset -g batt" and "pmset -g".
func parsePmset(batt, settings string) State {
	s := State{Percent: -1}
	s.HasBattery = strings.Contains(batt, "InternalBattery")
	s.OnBattery = s.HasBattery && strings.Contains(batt, "'Battery Power'")
	if m := percentRe.FindStringSubmatch(batt); s.HasBattery && m != nil {
		s.Percent, _ = strconv.Atoi(m[1])
	}
	for _, m := range lowPowerRe.FindAllStringSubmatch(settings, -1) {
		if m[2] == "1" { // lowpowermode 1, or powermode 1 (= Low Power) on newer Macs
			s.Saver = true
		}
	}
	return s
}
