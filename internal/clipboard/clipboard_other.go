//go:build !windows

package clipboard

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Write puts text on the clipboard.
func Write(text string) error {
	var tools [][]string
	if runtime.GOOS == "darwin" {
		tools = [][]string{{"/usr/bin/pbcopy"}}
	} else {
		tools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, t := range tools {
		cmd := exec.Command(t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return nil
		}
	}
	return errors.New("no clipboard tool found")
}
