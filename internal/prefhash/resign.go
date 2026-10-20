package prefhash

import (
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// Resign recomputes every legacy MAC listed in doc's protection.macs and the
// super_mac, for this calculator's device id. When dropEncrypted is true, the
// os_crypt-based *_encrypted_hash entries and super_encrypted_hash are
// removed (they cannot be valid on another device); Helium then rebuilds them
// from the now-valid legacy MACs (verified in phase 0).
//
// extraPaths are added to protection.macs so newly written protected values
// get a MAC too. It returns the number of MACs written.
func Resign(doc map[string]any, c Calculator, dropEncrypted bool, extraPaths ...string) int {
	protection, _ := doc["protection"].(map[string]any)
	if protection == nil {
		protection = map[string]any{}
		doc["protection"] = protection
	}
	macs, _ := protection["macs"].(map[string]any)
	if macs == nil {
		macs = map[string]any{}
		protection["macs"] = macs
	}
	for _, p := range extraPaths {
		chromejson.Set(macs, p, "")
	}
	n := 0
	var visit func(node map[string]any, prefix string)
	visit = func(node map[string]any, prefix string) {
		for k, v := range node {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if strings.HasSuffix(k, "_encrypted_hash") {
				if dropEncrypted {
					delete(node, k)
				}
				continue
			}
			switch t := v.(type) {
			case string:
				node[k] = c.MAC(p, ValueString(chromejson.Lookup(doc, p)))
				n++
			case map[string]any:
				visit(t, p)
			}
		}
	}
	visit(macs, "")
	protection["super_mac"] = c.MAC("", ValueString(macs))
	if dropEncrypted {
		delete(protection, "super_encrypted_hash")
	}
	return n
}
