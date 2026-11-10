package prefhash

import (
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// Verify reports whether this calculator reproduces the protection Helium
// itself wrote into doc (a Secure Preferences file as Helium left it): the
// super_mac over all MACs, and each MAC over its value.
//
// It is the safety check for writing Secure Preferences on any Helium
// version. If Helium ever changes how it signs settings, our MACs would not
// match, Helium would reset what we wrote ("Some settings were reset"), and
// switch extensions off. A file that does not verify is never written; the
// caller pauses settings and extensions for that profile instead. A file
// without a super_mac (never protected yet) verifies.
func Verify(doc map[string]any, c Calculator) bool {
	protection, _ := doc["protection"].(map[string]any)
	if protection == nil {
		return true
	}
	super, _ := protection["super_mac"].(string)
	macs, _ := protection["macs"].(map[string]any)
	if super == "" || macs == nil {
		return true
	}
	if c.MAC("", ValueString(macs)) != super {
		return false
	}
	ok := true
	var visit func(node map[string]any, prefix string)
	visit = func(node map[string]any, prefix string) {
		for k, v := range node {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if strings.HasSuffix(k, "_encrypted_hash") {
				continue // os_crypt-based (a value or a whole group): not ours to check
			}
			switch t := v.(type) {
			case string:
				if t != "" && c.MAC(p, ValueString(chromejson.Lookup(doc, p))) != t {
					ok = false
				}
			case map[string]any:
				visit(t, p)
			}
		}
	}
	visit(macs, "")
	return ok
}
