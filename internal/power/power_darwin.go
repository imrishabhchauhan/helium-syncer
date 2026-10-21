package power

import "os/exec"

// pmset ships with every macOS version, on Intel and Apple silicon.
func read() (State, error) {
	batt, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
	if err != nil {
		return State{}, err
	}
	settings, _ := exec.Command("/usr/bin/pmset", "-g").Output()
	return parsePmset(string(batt), string(settings)), nil
}
