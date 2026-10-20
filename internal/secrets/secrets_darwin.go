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

// Load reads what Save stored.
func Load(_ string, name string) ([]byte, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-a", name, "-s", service, "-w").Output()
	if err != nil {
		return nil, ErrNotFound
	}
	return hex.DecodeString(strings.TrimSpace(string(out)))
}

// Delete removes a stored secret.
func Delete(_ string, name string) error {
	exec.Command("/usr/bin/security", "delete-generic-password", "-a", name, "-s", service).Run()
	return nil
}
