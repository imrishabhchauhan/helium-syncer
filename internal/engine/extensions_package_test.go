package engine

import "testing"

// Helium updates extensions in place, so the version installed here soon
// differs from the one whose package was uploaded. Two passes run over the
// same extensions, and both must come to the same answer: when one fell
// back to the older package and the other did not, every sync reported the
// same extensions as changed and sent them again. The owner's Mac sent the
// same four every five minutes, for ever.
func TestAnUpdatedExtensionIsNotSentAgainOnEverySync(t *testing.T) {
	blobs := map[string]string{"abc@1.0": "blob-one"}

	// The pass that may upload falls back to the package already there.
	first := packageFor(blobs, "abc", "2.0")
	if first != "blob-one" {
		t.Fatalf("the uploaded package was not reused, got %q", first)
	}
	// The pass that only records must say the same thing.
	second := packageFor(blobs, "abc", "2.0")
	if second != first {
		t.Errorf("the two passes disagree: %q and %q, so this extension is sent again every sync", first, second)
	}
	if blobs["abc@2.0"] != "blob-one" {
		t.Errorf("the answer was not remembered for version 2.0, got %q", blobs["abc@2.0"])
	}
}

// With several older packages the answer must be the same on every run.
// Going over the map as it comes picks a different one each time, which is
// the same loop again.
func TestTheAnswerIsTheSameOnEveryRun(t *testing.T) {
	want := ""
	for i := 0; i < 20; i++ {
		blobs := map[string]string{"abc@1.0": "one", "abc@1.5": "two", "abc@2.0": "three", "zzz@9.9": "other"}
		got := packageFor(blobs, "abc", "3.0")
		if want == "" {
			want = got
		}
		if got != want {
			t.Fatalf("run %d answered %q, run 1 answered %q", i+1, got, want)
		}
	}
}

// An extension with nothing uploaded has no package, and nothing is
// invented for it.
func TestNothingUploadedMeansNoPackage(t *testing.T) {
	blobs := map[string]string{"other@1.0": "blob"}
	if got := packageFor(blobs, "abc", "1.0"); got != "" {
		t.Errorf("got %q for an extension that was never uploaded", got)
	}
	if _, there := blobs["abc@1.0"]; there {
		t.Error("an empty answer must not be remembered")
	}
}
