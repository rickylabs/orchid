package main

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Stage evidence is deterministic over an injected clock: first arrival wins,
// repeats are ignored, the record is bounded and carries no identifiers.
func TestLaunchStageClockDeterministic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tick := func(d time.Duration) { now = now.Add(d) }
	c := newLaunchStageClock(func() time.Time { return now })
	tick(30 * time.Millisecond)
	c.mark(stageMarkThreadPrepared)
	tick(2 * time.Second)
	c.mark(stageMarkRegistered)
	tick(time.Second)
	c.mark(stageMarkRegistered) // repeat ignored
	c.count(stageCountAttachmentReads)
	c.count(stageCountAttachmentReads)
	tick(500 * time.Millisecond)
	c.mark(stageMarkAttached)
	got := c.summary()
	want := "thread-prepared=30ms registered=2000ms attachment-proven=1500ms total=3530ms attachment-reads=2"
	if got != want {
		t.Fatalf("summary = %q\nwant      %q", got, want)
	}
	for i := 0; i < stageMaxMarks+5; i++ {
		c.mark(fmt.Sprintf("extra-%d", i))
	}
	if n := strings.Count(c.summary(), "ms "); n > stageMaxMarks+1 {
		t.Fatalf("stage record unbounded: %d marks", n)
	}
	var none *launchStageClock
	none.mark(stageMarkReady)
	none.count(stageCountWakes)
	if none.summary() != "" || newLaunchStageClock(nil).summary() != "" {
		t.Fatal("nil or empty clock must log nothing")
	}
	if !regexp.MustCompile(`^([a-z-]+=[0-9]+(ms)? ?)+$`).MatchString(got) {
		t.Fatal("stage summary must hold only stage names, durations and counts")
	}
}

// Opt-in staged benchmark of Orchid's acceptance stages over the real bridge
// and a synthetic AF_UNIX daemon: subscribed wakes versus interval polling
// for a turn whose user item materializes late. Run with
// ORCHID_STAGE_BENCH=<iterations>. It reports; it asserts no timing.
func TestCodexAcceptanceStageBenchmark(t *testing.T) {
	n := 0
	if _, err := fmt.Sscan(os.Getenv("ORCHID_STAGE_BENCH"), &n); err != nil || n <= 0 {
		t.Skip("set ORCHID_STAGE_BENCH=<iterations> to record the staged benchmark")
	}
	const interval, materialize = 250 * time.Millisecond, 40 * time.Millisecond
	for _, mode := range []string{"subscribed", "polling"} {
		samples := map[string][]time.Duration{}
		wakes := 0
		for i := 0; i < n; i++ {
			h := newAccHarness(t)
			h.acc.every = interval
			h.f.resumeRefused = mode == "polling"
			clock := newLaunchStageClock(nil)
			h.host.Stages = clock
			h.f.page = func(f *accFixture, i int) ([]any, any) {
				turns, cursor := accDefaultPage(f, i)
				if i == 0 && f.getSent() != "" {
					turns[0] = accTurn(accNewTurn, "inProgress")
				}
				return turns, cursor
			}
			h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
				if full == 0 {
					return &accReply{after: func(_ net.Conn) {
						time.Sleep(materialize)
						if f.resumeRefused {
							return // an unsubscribed connection receives no thread signals
						}
						f.push(map[string]any{"method": "item/started", "params": map[string]any{"threadId": f.run.NativeSessionID, "turnId": accNewTurn,
							"item": accUser(f.getSent()), "startedAtMs": 1}})
					}}
				}
				return nil
			}
			accAccepted(t, h, h.accept(accSent))
			clock.mu.Lock()
			for _, m := range clock.marks {
				samples[m.name] = append(samples[m.name], m.since)
			}
			wakes += clock.counts[stageCountWakes]
			clock.mu.Unlock()
		}
		var line []string
		for _, stage := range []string{stageMarkSubscribed, stageMarkBaseline, stageMarkAttempted, stageMarkSubmitAcked, stageMarkFirstTurn, stageMarkAccepted} {
			d := samples[stage]
			if len(d) == 0 {
				continue
			}
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
			line = append(line, fmt.Sprintf("%s p50=%s p90=%s", stage, d[len(d)/2].Round(100*time.Microsecond), d[len(d)*9/10].Round(100*time.Microsecond)))
		}
		t.Logf("stage-bench mode=%s n=%d interval=%s materialize=%s wakes=%d | %s", mode, n, interval, materialize, wakes, strings.Join(line, " | "))
	}
}
