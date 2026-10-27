package main

import (
	"context"
	"strings"
)

const codexConfigValueWrite = "config/value/write"
const codexTrustedCheckout = "trusted"

// This private request is the only config write accepted by the goal transport.
// Keep the path as one quoted key segment; the API unescapes punctuation rather
// than JSON escapes, so encode only backslashes and quotes, leaving Unicode intact.
type codexCheckoutTrustWrite struct {
	KeyPath       string `json:"keyPath"`
	Value         any    `json:"value"`
	MergeStrategy string `json:"mergeStrategy"`
	cwd           string
}

func codexCheckoutTrustKey(cwd string) string {
	return `projects."` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(cwd) + `"`
}

func (w codexCheckoutTrustWrite) valid() bool {
	if !validRemoteCwd(w.cwd) || w.KeyPath != codexCheckoutTrustKey(w.cwd) || w.MergeStrategy != "upsert" {
		return false
	}
	if w.Value == nil {
		return true
	}
	v, ok := w.Value.(map[string]string)
	return ok && len(v) == 1 && v["trust_level"] == codexTrustedCheckout
}

// Grant and removal share the exact checkout key and native response checks.
// Null removes the project entry; no ancestor or sibling project is rewritten.
func (p *goalRPC) setCheckoutTrust(cwd string, trusted bool) error {
	write := codexCheckoutTrustWrite{KeyPath: codexCheckoutTrustKey(cwd), MergeStrategy: "upsert", cwd: cwd}
	if trusted {
		write.Value = map[string]string{"trust_level": codexTrustedCheckout}
	}
	raw, err := p.request(codexConfigValueWrite, write)
	var result struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err != nil || decodeNativeJSON(raw, &result) != nil || result.Status != "ok" || result.Version == "" {
		return goalError("remote-control-checkout-trust-unconfirmed")
	}
	return nil
}

func (h Host) removeCheckoutTrust(ctx context.Context, cwd string) error {
	// Trust belongs to the checkout, not a possibly archived native thread.
	// Cleanup must still work after the thread or its staged binding is gone.
	h.RemoteRun = nil
	return h.withCanonicalConnection(ctx, "", func(p *goalRPC) error {
		return p.setCheckoutTrust(cwd, false)
	})
}
