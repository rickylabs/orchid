package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAGYScopedSettingsStrictBoundary(t *testing.T) {
	input := `{"model":"fixture-model","toolPermission":"ask","trustedWorkspaces":["/fixture/prior"],"nested":{"allow":false,"items":[1,2]}}`
	merged, err := agyScopedSettings([]byte(input), "/fixture/checkout")
	var original, got map[string]json.RawMessage
	_ = json.Unmarshal([]byte(input), &original)
	if err != nil || json.Unmarshal(merged, &got) != nil {
		t.Fatal("valid settings were refused")
	}
	for key, value := range original {
		if key != "trustedWorkspaces" && string(got[key]) != string(value) {
			t.Fatal("non-trust field changed")
		}
	}
	for _, raw := range []string{"", "null", "[]", "\xff", "{\"model\":\"\xff\"}", `{"model":"first","model":"second"}`, `{"trustedWorkspaces":null}`, `{"trustedWorkspaces":{}}`, input + strings.Repeat(" ", 1024*1024)} {
		if _, err := agyScopedSettings([]byte(raw), "/fixture/checkout"); err != matrixReason("agy-settings-unavailable") {
			t.Fatal("invalid or oversized settings were admitted")
		}
	}
}

func TestAGYScopedTrustPreservesSettingsAndOnlyTrustsCheckout(t *testing.T) {
	h, calls := registrationHost(t, "")
	cwd := filepath.Join(t.TempDir(), `repo.v1 "quoted" $(touch CANARY) & café`)
	writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
	originalPath := filepath.Join(h.Home, ".gemini", "antigravity-cli", "settings.json")
	original, _ := os.ReadFile(originalPath)
	pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "agy", Overrides{Model: "fixture-model", Effort: "high"}, registrationReceipt(t, "agy", Overrides{Model: "fixture-model", Effort: "high"}))
	if err != nil || pane != "w1:p1" || ws != "w1" {
		t.Fatal("exact scoped trust did not reach synthetic readiness", err)
	}
	if after, _ := os.ReadFile(originalPath); string(after) != string(original) {
		t.Fatal("standing settings changed")
	}
	credentialSource := filepath.Join(filepath.Dir(originalPath), "antigravity-oauth-token")
	credentialLink := filepath.Join(cwd, ".divybot-agy", "antigravity-oauth-token")
	if target, err := os.Readlink(credentialLink); err != nil || target != credentialSource {
		t.Fatal("native authentication was copied, relocated or not bound by reference")
	}
	if credential, err := os.ReadFile(credentialSource); err != nil || string(credential) != "SYNTHETIC-AUTH-REFERENCE" {
		t.Fatal("preflight changed native credential bytes")
	}
	onboardingSource := filepath.Join(filepath.Dir(originalPath), "cache", "onboarding.json")
	onboardingLink := filepath.Join(cwd, ".divybot-agy", "cache", "onboarding.json")
	if target, err := os.Readlink(onboardingLink); err != nil || target != onboardingSource {
		t.Fatal("native onboarding was copied, invented or not bound by reference")
	}
	path := filepath.Join(cwd, ".divybot-agy", "settings.json")
	raw, err := os.ReadFile(path)
	var got map[string]any
	if err != nil || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got["trustedWorkspaces"], []any{cwd}) || got["model"] != "fixture-default" || got["toolPermission"] != "always-proceed" {
		t.Fatal("trust scope or non-trust settings changed")
	}
	if stat, err := os.Stat(path); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("scoped settings are not private")
	}
	if stat, err := os.Stat(filepath.Dir(path)); err != nil || stat.Mode().Perm() != 0700 {
		t.Fatal("scoped settings directory is not private")
	}
	for _, call := range calls() {
		if call[0] == "agent" && call[1] == "prompt" || call[0] == "pane" && call[1] == "send-keys" {
			t.Fatal("startup sent input")
		}
	}
	if _, err := os.Stat("CANARY"); !os.IsNotExist(err) {
		t.Fatal("checkout spelling executed shell content")
	}
	if _, err := h.prepareAGYTrust(context.Background(), cwd); err == nil {
		t.Fatal("prior state was overwritten/reused")
	}
}

func TestAGYSettingsAndScopeFailClosedBeforeSeat(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "duplicate", "null", "bad-trust", "oversized", "symlink", "scope-symlink", "unclean"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := registrationHost(t, "")
			cwd := t.TempDir()
			writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
			settings := filepath.Join(h.Home, ".gemini", "antigravity-cli", "settings.json")
			value := ""
			switch mode {
			case "missing":
				_ = os.Remove(settings)
			case "malformed":
				value = "PRIVATE-AGY-CANARY"
			case "duplicate":
				value = `{"model":"first","model":"second"}`
			case "null":
				value = "null"
			case "bad-trust":
				value = `{"trustedWorkspaces":"/fixture/parent"}`
			case "oversized":
				value = `{"trustedWorkspaces":[]}` + strings.Repeat(" ", 1024*1024)
			case "symlink":
				_ = os.Symlink(t.TempDir(), filepath.Join(cwd, ".divybot-agy"))
			case "scope-symlink":
				linked := filepath.Join(t.TempDir(), "linked")
				if os.Symlink(cwd, linked) != nil {
					t.Fatal("synthetic checkout symlink unavailable")
				}
				cwd = linked
			case "unclean":
				cwd += "//"
			}
			if value != "" && os.WriteFile(settings, []byte(value), 0600) != nil {
				t.Fatal("synthetic settings unavailable")
			}
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "agy", Overrides{}, registrationReceipt(t, "agy", Overrides{}))
			if err == nil || pane != "" || ws != "" || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("invalid settings/scope caused a seat or leaked input")
			}
			if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
				t.Fatal("invalid trust preflight reached Herdr")
			}
		})
	}
}

func TestAGYTrustAndLoginBlockedBeforeGoal(t *testing.T) {
	for _, mode := range []string{"agy-trust", "agy-login", "agy-onboarding", "agy-native-blocked", "agy-timeout", "agy-timeout-unknown", "agy-timeout-name-changed", "agy-foreign", "agy-name", "agy-pane", "agy-workspace", "agy-cwd", "agy-working", "agy-unknown", "agy-changing", "agy-not-ready", "agy-unreadable", "agy-malformed", "agy-oversized", "agy-empty", "agy-utf8", "agy-unknown-screen"} {
		t.Run(mode, func(t *testing.T) {
			h, calls := registrationHost(t, mode)
			cwd := t.TempDir()
			writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "agy", Overrides{}, registrationReceipt(t, "agy", Overrides{}))
			want := "registration_failed"
			if mode == "agy-trust" || mode == "agy-login" || mode == "agy-onboarding" || mode == "agy-timeout" || mode == "agy-native-blocked" {
				want = "startup_blocked"
			}
			if mode == "agy-timeout-unknown" || mode == "agy-timeout-name-changed" {
				want = "startup_timeout"
			}
			if err == nil || registrationFailureKind(err) != want || pane != "w1:p1" || ws != "w1" || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("AGY startup lost blocking, ownership or privacy", registrationFailureKind(err))
			}
			starts := 0
			for _, call := range calls() {
				if call[0] == "agent" && call[1] == "start" {
					starts++
				} else if call[0] == "agent" && call[1] == "get" || call[0] == "pane" && call[1] == "read" {
					if call[2] != pane {
						t.Fatal("diagnostic read escaped owned pane")
					}
				} else if call[0] != "workspace" && !(call[0] == "pane" && call[1] == "run") {
					t.Fatal("startup blocker received input")
				}
			}
			if starts != 1 {
				t.Fatal("startup was retried")
			}
		})
	}
}

func TestAGYReadinessPreservesCancellation(t *testing.T) {
	h, _ := registrationHost(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if registrationFailureKind(h.agyRegistrationCheck(ctx, "fixture-agent", "/fixture/repo", "w1:p1", "w1", true)) != "startup_cancelled" {
		t.Fatal("cancelled registration was relabelled or admitted")
	}
	if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
		t.Fatal("cancelled readiness check reached a native effect")
	}
}

func TestAGYCredentialReferenceFailsClosedOrPreservesNativeAuthInputs(t *testing.T) {
	for _, mode := range []string{"missing", "dangling", "directory", "unreadable", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := registrationHost(t, "")
			cwd := t.TempDir()
			writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
			original := filepath.Join(h.Home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
			if mode == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read permission-denied fixture")
			}
			if mode != "refresh" && mode != "unreadable" {
				_ = os.Remove(original)
			}
			if mode == "dangling" {
				_ = os.Symlink(filepath.Join(t.TempDir(), "absent"), original)
			}
			if mode == "directory" {
				_ = os.Mkdir(original, 0700)
			}
			if mode == "unreadable" {
				_ = os.Chmod(original, 0000)
			}
			_, err := h.prepareAGYTrust(context.Background(), cwd)
			if mode == "dangling" || mode == "directory" || mode == "unreadable" {
				if err == nil {
					t.Fatal("invalid native credential reference was admitted")
				}
				return
			}
			if err != nil {
				t.Fatal("existing native authentication inputs were refused")
			}
			link := filepath.Join(cwd, ".divybot-agy", "antigravity-oauth-token")
			if mode == "missing" {
				// A native keyring/GCP input is not inferred or replaced with a file.
				if _, err := os.Lstat(link); !os.IsNotExist(err) {
					t.Fatal("absent authentication was invented")
				}
				return
			}
			// Measured native refresh uses os.WriteFile and follows this reference.
			if os.WriteFile(link, []byte("SYNTHETIC-REFRESH"), 0600) != nil {
				t.Fatal("synthetic native refresh failed")
			}
			if value, err := os.ReadFile(original); err != nil || string(value) != "SYNTHETIC-REFRESH" {
				t.Fatal("refresh relocated credential bytes")
			}
			if target, err := os.Readlink(link); err != nil || target != original {
				t.Fatal("refresh replaced the credential reference")
			}
		})
	}
}

func TestAGYOnboardingReferencePreservesNativeState(t *testing.T) {
	for _, mode := range []string{"missing", "dangling", "directory", "unreadable", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := registrationHost(t, "")
			cwd := t.TempDir()
			writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
			original := filepath.Join(h.Home, ".gemini", "antigravity-cli", "cache", "onboarding.json")
			if mode == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read permission-denied fixture")
			}
			if mode != "refresh" && mode != "unreadable" {
				_ = os.Remove(original)
			}
			if mode == "dangling" {
				_ = os.Symlink(filepath.Join(t.TempDir(), "absent"), original)
			}
			if mode == "directory" {
				_ = os.Mkdir(original, 0700)
			}
			if mode == "unreadable" {
				_ = os.Chmod(original, 0000)
			}
			_, err := h.prepareAGYTrust(context.Background(), cwd)
			if mode == "dangling" || mode == "directory" || mode == "unreadable" {
				if err == nil {
					t.Fatal("invalid native onboarding reference was admitted")
				}
				return
			}
			if err != nil {
				t.Fatal("optional native onboarding state was refused")
			}
			link := filepath.Join(cwd, ".divybot-agy", "cache", "onboarding.json")
			if mode == "missing" {
				if _, err := os.Lstat(link); !os.IsNotExist(err) {
					t.Fatal("onboarding completion was invented")
				}
				return
			}
			// Synthetic native cache refresh follows the reference too.
			value := `{"consumerOnboardingComplete":false}`
			if os.WriteFile(link, []byte(value), 0600) != nil {
				t.Fatal("synthetic native cache refresh failed")
			}
			if got, err := os.ReadFile(original); err != nil || string(got) != value {
				t.Fatal("refresh relocated onboarding state")
			}
			if target, err := os.Readlink(link); err != nil || target != original {
				t.Fatal("refresh replaced the onboarding reference")
			}
		})
	}
}

func TestAGYComposerRequiresBoundedPositiveEvidence(t *testing.T) {
	for _, screen := range []string{"", "unknown idle screen", "\xff ? for shortcuts", strings.Repeat(" ", 64*1024) + "? for shortcuts"} {
		if agyStartupComposer(screen) {
			t.Fatal("uncertain screen admitted as composer")
		}
	}
	if !agyStartupComposer("Antigravity\n > \n ? for shortcuts") {
		t.Fatal("native source-backed idle hint refused")
	}
}

func TestAGYCanonicalScopeRejectsUnsafeSpelling(t *testing.T) {
	for _, cwd := range []string{"relative", "/", "/fixture//checkout", "/fixture/../checkout", "/fixture/\ncheckout", "/fixture/\xffcheckout", "/" + strings.Repeat("x", 4096)} {
		if agyTrustScope(cwd) {
			t.Fatal("unsafe spelling admitted by trust scope boundary")
		}
		h, _ := registrationHost(t, "")
		if _, err := h.prepareAGYTrust(context.Background(), cwd); err == nil {
			t.Fatal("unsafe trust scope was admitted")
		}
		if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
			t.Fatal("unsafe scope caused a seat")
		}
	}
}
