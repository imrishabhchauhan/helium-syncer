package secrets

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound means nothing is stored under that name.
var ErrNotFound = errors.New("secrets: not found")

const service = "Helium Syncer"

// Save stores data in the login Keychain. The value is passed to the
// security tool on stdin (interactive mode), never on the command line.
func Save(_ string, name string, data []byte) error {
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -a %q -s %q -w %s\n", name, service, hex.EncodeToString(data)))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("secrets: keychain: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// notFoundExit is what the security tool returns when the keychain holds
// no such item. Every other failure means something else: most often 36,
// "user interaction is not allowed", which is what a program started
// outside the person's desktop session gets.
const notFoundExit = 44

// Load reads what Save stored. It tells "nothing saved" apart from "saved
// but it would not open": they used to look the same, and a program that
// could not open the keychain then told the person they had never joined a
// sync, which sends them to join again and replace the sync they have.
func Load(_ string, name string) ([]byte, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-a", name, "-s", service, "-w")
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == notFoundExit {
			return nil, ErrNotFound
		}
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("secrets: keychain: %s", msg)
	}
	return hex.DecodeString(strings.TrimSpace(string(out)))
}

// Delete removes a stored secret.
func Delete(_ string, name string) error {
	exec.Command("/usr/bin/security", "delete-generic-password", "-a", name, "-s", service).Run()
	return nil
}
