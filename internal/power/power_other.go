//go:build !windows && !darwin

package power

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func read() (State, error) {
	s := State{Percent: -1}
	dirs, _ := filepath.Glob("/sys/class/power_supply/*")
	for _, d := range dirs {
		kind, _ := os.ReadFile(filepath.Join(d, "type"))
		if strings.TrimSpace(string(kind)) != "Battery" {
			continue
		}
		s.HasBattery = true
		status, _ := os.ReadFile(filepath.Join(d, "status"))
		if strings.TrimSpace(string(status)) == "Discharging" {
			s.OnBattery = true
		}
		if c, err := os.ReadFile(filepath.Join(d, "capacity")); err == nil {
			s.Percent, _ = strconv.Atoi(strings.TrimSpace(string(c)))
		}
	}
	return s, nil
}
