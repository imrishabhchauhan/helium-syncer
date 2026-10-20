package helium

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckVersions(t *testing.T) {
	ok := "155.0.8059.84"
	if err := CheckVersions(ok, map[string]string{"Mac": ok}); err != nil {
		t.Fatalf("same tested version must pass: %v", err)
	}
	var ge *GateError
	if err := CheckVersions(ok, map[string]string{"Mac": "156.0.1.1"}); !errors.As(err, &ge) || !strings.Contains(err.Error(), "Mac") {
		t.Fatalf("untested peer must pause with the device name: %v", err)
	}
	if err := CheckVersions("150.0.0.1", nil); !errors.As(err, &ge) {
		t.Fatalf("untested local version must pause: %v", err)
	}
	Tested["154.0.1.1"] = true
	defer delete(Tested, "154.0.1.1")
	err := CheckVersions(ok, map[string]string{"Office PC": "154.0.1.1"})
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "Update Helium on the older device") {
		t.Fatalf("different tested versions must ask to update: %v", err)
	}
}
