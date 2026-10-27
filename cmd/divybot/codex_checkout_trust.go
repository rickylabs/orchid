package main

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const codexConfigRead = "config/read"
const codexConfigValueWrite = "config/value/write"
const codexTrustedCheckout = "trusted"

var errCodexTrustRemoval = goalError("remote-control-checkout-trust-removal-unconfirmed")

// Both grant and removal accept only a complete, positive issue checkout.
func validTrustCheckout(cwd string) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(cwd), "issue-"))
	return err == nil && n > 0 && validJobCheckout(cwd, n)
}

// Reconciliation reads the daemon's config only, never a thread or project layer.
type codexCheckoutTrustRead struct {
	IncludeLayers bool `json:"includeLayers"`
}

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
	if !validTrustCheckout(w.cwd) || w.KeyPath != codexCheckoutTrustKey(w.cwd) || w.MergeStrategy != "upsert" {
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

// The current and compatibility roots are the complete sweep boundary. A config
// change cannot authorize recursively clearing projects beneath a parent root.
func (h Host) checkoutTrustRoots() ([]string, error) {
	disk := h.DiskWorkdirRoot
	if disk == "" {
		disk = defaultDiskWorkdirRoot
	}
	legacy := filepath.Dir(h.legacyCheckout(1))
	if !validRemoteCwd(disk) || !validRemoteCwd(legacy) {
		return nil, goalError("checkout-root-invalid")
	}
	if disk == legacy {
		return []string{disk}, nil
	}
	return []string{disk, legacy}, nil
}

// Reconcile from native config so even a crash between grant and Job save has
// durable recovery evidence. Preserve retained jobs, other roots and untrusted
// entries; retry unconfirmed removals on the next existing lifecycle tick.
func (p *goalRPC) reconcileCheckoutTrust(roots []string, retained map[string]bool) error {
	for _, root := range roots {
		if !validRemoteCwd(root) {
			return goalError("checkout-root-invalid")
		}
	}
	raw, err := p.request(codexConfigRead, codexCheckoutTrustRead{})
	var result struct {
		Config *struct {
			Projects map[string]json.RawMessage `json:"projects"`
		} `json:"config"`
	}
	if err != nil || decodeNativeJSON(raw, &result) != nil || result.Config == nil {
		return goalError("remote-control-checkout-trust-unconfirmed")
	}
	paths := make([]string, 0, len(result.Config.Projects))
	for cwd := range result.Config.Projects {
		paths = append(paths, cwd)
	}
	sort.Strings(paths)
	var failure error
	for _, cwd := range paths {
		if !validTrustCheckout(cwd) || retained[cwd] {
			continue
		}
		owned := false
		for _, root := range roots {
			if filepath.Dir(cwd) == root { // guard:trust-direct-checkout
				owned = true
			}
		}
		if !owned {
			continue
		}
		var project struct {
			TrustLevel string `json:"trust_level"`
		}
		if decodeNativeJSON(result.Config.Projects[cwd], &project) != nil {
			failure = goalError("remote-control-checkout-trust-unconfirmed")
			continue
		}
		if project.TrustLevel != codexTrustedCheckout {
			continue
		}
		if err := p.setCheckoutTrust(cwd, false); err != nil { // guard:trust-orphan-removal
			failure = err
		}
	}
	return failure
}

// Startup and tick call this synchronously before admission; no sweep overlaps
// a grant still being converted into a Job by the serialized dispatcher loop.
func (c *Coord) reconcileCheckoutTrust(ctx context.Context) {
	if c.dry || ctx.Err() != nil || c.st == nil {
		return
	}
	retained := map[string]map[string]bool{}
	codexJobs := map[string]bool{}
	c.st.mu.Lock()
	for _, j := range c.st.Jobs {
		h, ok := c.hosts[j.Host]
		if !ok {
			continue
		}
		if j.Agent == "codex" {
			codexJobs[j.Host] = true
		}
		if j.stopUnconfirmed() {
			continue // a bounded release is no longer a trust owner
		}
		if retained[j.Host] == nil {
			retained[j.Host] = map[string]bool{}
		}
		retained[j.Host][h.jobCheckout(j)] = true
	}
	c.st.mu.Unlock()
	for name, h := range c.hosts {
		// Config-loaded hosts retain this pointer even with Codex disabled, so
		// switching off new launches does not strand their earlier trust entries.
		if h.RemoteControl == nil && !codexJobs[name] {
			continue
		}
		roots, err := h.checkoutTrustRoots()
		if err == nil {
			check, done := context.WithTimeout(ctx, 10*time.Second)
			h.RemoteRun = nil
			err = h.withCanonicalConnection(check, "", func(p *goalRPC) error {
				return p.reconcileCheckoutTrust(roots, retained[name])
			})
			done()
		}
		if err != nil {
			log.Printf("codex checkout trust reconciliation unconfirmed reason=%s", closedGoalReason(err))
		}
	}
}

// Used both by normal checkout cleanup and the bounded native-stop release.
// Invalid checkouts keep the old skip behavior; only failed Codex revocation
// may withhold teardown confirmation.
func (h Host) revokeJobCheckoutTrust(ctx context.Context, j *Job) error {
	if j.Agent != "codex" || j.RemoteControl == nil {
		return nil
	}
	cwd := h.jobCheckout(j)
	if !validJobCheckout(cwd, j.Issue) {
		return nil
	}
	if j.RemoteControl.Cwd != cwd || h.removeCheckoutTrust(ctx, cwd) != nil {
		return errCodexTrustRemoval
	}
	return nil
}
