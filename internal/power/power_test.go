package power

import "testing"

func TestParseWindows(t *testing.T) {
	cases := []struct {
		name string
		st   systemPowerStatus
		want State
	}{
		{"desktop", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}, State{Percent: -1}},
		{"laptop plugged in", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 80}, State{HasBattery: true, Percent: 80}},
		{"laptop on battery", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 64}, State{HasBattery: true, OnBattery: true, Percent: 64}},
		{"battery saver", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 2, BatteryLifePercent: 18, SystemStatusFlag: 1}, State{HasBattery: true, OnBattery: true, Saver: true, Percent: 18}},
		{"unknown", systemPowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 255}, State{Percent: -1}},
	}
	for _, c := range cases {
		if got := parseWindows(c.st); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParsePmset(t *testing.T) {
	const onBattery = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=26345571)\t57%; discharging; 4:12 remaining present: true\n"
	const charging = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=26345571)\t91%; charging; 0:20 remaining present: true\n"
	const desktop = "Now drawing from 'AC Power'\n"
	const lowPower = "System-wide power settings:\nCurrently in use:\n standby              1\n lowpowermode         1\n sleep                1\n"
	const newerLowPower = "Currently in use:\n powermode            1\n"
	const normal = "Currently in use:\n lowpowermode         0\n powernap             1\n"
	cases := []struct {
		name, batt, settings string
		want                 State
	}{
		{"on battery", onBattery, normal, State{HasBattery: true, OnBattery: true, Percent: 57}},
		{"charging", charging, normal, State{HasBattery: true, Percent: 91}},
		{"desktop Mac", desktop, normal, State{Percent: -1}},
		{"low power mode", onBattery, lowPower, State{HasBattery: true, OnBattery: true, Saver: true, Percent: 57}},
		{"powermode 1", onBattery, newerLowPower, State{HasBattery: true, OnBattery: true, Saver: true, Percent: 57}},
	}
	for _, c := range cases {
		if got := parsePmset(c.batt, c.settings); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestLow(t *testing.T) {
	for _, c := range []struct {
		s    State
		want bool
	}{
		{State{Percent: -1}, false},
		{State{HasBattery: true, Percent: 10}, false}, // plugged in
		{State{HasBattery: true, OnBattery: true, Percent: 21}, false},
		{State{HasBattery: true, OnBattery: true, Percent: 20}, true},
		{State{HasBattery: true, OnBattery: true, Percent: 90, Saver: true}, true},
		{State{HasBattery: true, OnBattery: true, Percent: -1}, false},
	} {
		if got := c.s.Low(); got != c.want {
			t.Errorf("%+v: Low()=%v", c.s, got)
		}
	}
}

func TestReadDoesNotFail(t *testing.T) {
	s := Read()
	t.Logf("this computer: %+v", s)
	if !s.HasBattery && (s.OnBattery || s.Percent != -1) {
		t.Fatalf("no battery but %+v", s)
	}
}

func TestFake(t *testing.T) {
	for in, want := range map[string]State{
		"desktop":          {Percent: -1},
		"plugged:80":       {HasBattery: true, Percent: 80},
		"battery:70":       {HasBattery: true, OnBattery: true, Percent: 70},
		"battery:15:saver": {HasBattery: true, OnBattery: true, Percent: 15, Saver: true},
	} {
		t.Setenv("HELIUM_SYNCER_FAKE_POWER", in)
		if got := Read(); got != want {
			t.Errorf("%s: got %+v", in, got)
		}
	}
}
