package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Every attachment refusal keeps the closed outward reason and carries a
// typed outcome. Rows are synthetic, in the daemon's real shapes.
func TestAttachmentResumeRecordTypedOutcomes(t *testing.T) {
	const p, q = "pid:7:canonical", "pid:9:embedded"
	run := syntheticRemoteRun(t)
	run.ClientVersion = "9.1.0"
	th := run.NativeSessionID
	ok := []traceRow{createdRow(p, th), resumedRow(p, th, "252", "9.1.0"), proceededRow(p, "252")}
	for _, tc := range []struct {
		name string
		rows []traceRow
		want attachmentOutcome
	}{
		{"proven", ok, ""},
		{"two-tui-connections", append(append([]traceRow{}, ok...), resumedRow(p, th, "253", "9.1.0")), attachmentAmbiguous},
		{"two-creators", append([]traceRow{createdRow(q, th)}, ok...), attachmentAmbiguous},
		{"connection-id-reuse", append(append([]traceRow{}, ok...), resumedRow(q, th, "252", "9.1.0")), attachmentAmbiguous},
		{"foreign-producer", []traceRow{createdRow(p, th), resumedRow(q, th, "252", "9.1.0"), proceededRow(q, "252")}, attachmentConsumerChanged},
		{"other-client-version", []traceRow{createdRow(p, th), resumedRow(p, th, "252", "9.2.0"), proceededRow(p, "252")}, attachmentConsumerChanged},
		{"lost-acknowledgment", []traceRow{createdRow(p, th), resumedRow(p, th, "252", "9.1.0")}, attachmentDeadline},
		{"no-record", nil, attachmentDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", "")
			if tc.rows != nil {
				writeTrace(t, home, tc.rows...)
			}
			r := *run
			clock := newLaunchStageClock(nil)
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			err := Host{Name: "fixture-host", Home: home, Stages: clock}.awaitCodexResumeIdentity(ctx, &r)
			if got := attachmentOutcomeOf(err); got != tc.want {
				t.Fatalf("attachment outcome = %q, want %q (err=%v)", got, tc.want, err)
			}
			if tc.want == "" && (err != nil || r.Resume == nil) {
				t.Fatal("proven record not stored")
			}
			if err != nil && !errors.Is(err, goalError("remote-control-identity-unconfirmed")) {
				t.Fatalf("outward reason changed: %v", err)
			}
			if clock.counts[stageCountAttachmentReads] < 1 {
				t.Fatal("attachment reads not counted as stage evidence")
			}
		})
	}
	t.Run("unpinned-version", func(t *testing.T) {
		r := *run
		r.ClientVersion = ""
		err := Host{Name: "fixture-host", Home: t.TempDir()}.awaitCodexResumeIdentity(context.Background(), &r)
		if attachmentOutcomeOf(err) != attachmentUnreadable {
			t.Fatalf("unpinned client must be unreadable, got %v", err)
		}
	})
}

func TestAttachmentProcessContinuityTyped(t *testing.T) {
	stored := &remoteTUIProcess{PID: 4242, Fingerprint: strings.Repeat("cd", 32)}
	for _, tc := range []struct {
		name    string
		current *remoteTUIProcess
		readErr error
		want    attachmentOutcome
	}{
		{"same", &remoteTUIProcess{PID: 4242, Fingerprint: strings.Repeat("cd", 32)}, nil, ""},
		{"reused-pid", &remoteTUIProcess{PID: 4242, Fingerprint: strings.Repeat("ef", 32)}, nil, attachmentProcessReused},
		{"other-process", &remoteTUIProcess{PID: 4343, Fingerprint: strings.Repeat("cd", 32)}, nil, attachmentConsumerChanged},
		{"unreadable", nil, goalError("remote-control-identity-unconfirmed"), attachmentUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(context.Context) (*remoteTUIProcess, error) { return tc.current, tc.readErr }
			err := checkTUIProcess(context.Background(), read, stored)
			if got := attachmentOutcomeOf(err); got != tc.want || (tc.want == "") != (err == nil) {
				t.Fatalf("process outcome = %q (err=%v), want %q", got, err, tc.want)
			}
		})
	}
	if compareTUIProcess(nil, stored) != attachmentUnreadable {
		t.Fatal("missing stored process must be unreadable, never same")
	}
}

// The private log proof is never reported as vendor-native, and the named
// upstream gap stays attached to every authority.
func TestAttachmentAuthorityIsNeverVendorNative(t *testing.T) {
	proven := provenRun(t)
	if a := attachmentAuthorityOf(proven); a.Source != attachmentSourcePrivateLog || a.NativeCapability != nativeCapabilityMissing || a.Gap != codexAttachmentNativeGap {
		t.Fatalf("proven run authority = %+v", a)
	}
	unproven := provenRun(t)
	unproven.Resume = nil
	if a := attachmentAuthorityOf(unproven); a.Source != attachmentSourceNone {
		t.Fatalf("unproven run authority = %+v", a)
	}
	if a := attachmentAuthorityOf(nil); a.Source != attachmentSourceNone || a.NativeCapability != nativeCapabilityMissing {
		t.Fatalf("nil run authority = %+v", a)
	}
	for _, state := range []string{"", "proven-ish", "native"} {
		if resumeRecordOutcome(state, "") != attachmentUnreadable {
			t.Fatalf("unknown reader state %q must be unreadable", state)
		}
	}
	if resumeRecordOutcome("refused", "other") != attachmentUnreadable {
		t.Fatal("an unclassified refusal must be unreadable")
	}
}
