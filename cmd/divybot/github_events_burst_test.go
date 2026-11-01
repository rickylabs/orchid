package main

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func githubOwnedCheckFixture(t *testing.T) (*Coord, string) {
	t.Helper()
	c := newCoord(&Config{Inbox: "example/inbox", BotLogin: "fixture-bot"})
	c.st.Jobs[7] = &Job{Issue: 7, Repo: "example/inbox", Branch: "owned-branch"}
	return c, `{"repository":{"full_name":"example/inbox"},"check_run":{"id":%d,"check_suite":{"head_branch":"owned-branch"}}}`
}

func TestGitHubEventBurstHasNegativeControl(t *testing.T) {
	spacing := 60 * time.Millisecond
	for _, paced := range []bool{true, false} {
		name := "paced"
		policy := supervisionWakeTiming{debounce: 3 * time.Millisecond, minSpacing: spacing}
		if !paced {
			name, policy = "unpaced-negative", supervisionWakeTiming{}
		}
		t.Run(name, func(t *testing.T) {
			c, payload := githubOwnedCheckFixture(t)
			client, ctx := githubWakeFixture(t, c, policy)
			var pickups atomic.Int32
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					case <-ctx.Done():
						return
					case <-c.fleet.wake:
						pickups.Add(1) // exactly the channel receive that enters Coord.tick
					}
				}
			}()
			defer func() { close(stop); <-done }()
			started := time.Now()
			for n := range 60 {
				githubDeliver(t, client, "check_run", fmt.Sprint(n), fmt.Sprintf(payload, n))
			}
			producing := time.Since(started)
			<-time.After(2 * spacing)
			count, maximum := int(pickups.Load()), 2+int(producing/spacing)
			t.Logf("60 unique native checks: pickups=%d paced maximum=%d", count, maximum)
			if count == 0 || (paced && count > maximum) || (!paced && count <= maximum) {
				t.Fatal("burst control did not distinguish paced ticks from its unpaced negative")
			}
		})
	}
}

func TestGitHubEventDefaultBurstIsBounded(t *testing.T) {
	c, payload := githubOwnedCheckFixture(t)
	client, _ := githubWakeFixture(t, c)
	githubDeliver(t, client, "check_run", "initial", fmt.Sprintf(payload, 1))
	githubPickup(t, c)
	// The real loop is ready to receive again: an emptied channel alone cannot pace ticks.
	for n := 2; n <= 61; n++ {
		githubDeliver(t, client, "check_run", fmt.Sprint(n), fmt.Sprintf(payload, n))
	}
	select {
	case <-c.fleet.wake:
		t.Fatal("event burst exceeded one tick within the minimum-spacing window")
	case <-time.After(600 * time.Millisecond):
	}
}

func TestGitHubEventUnownedChecksDoNotWake(t *testing.T) {
	c, _ := githubOwnedCheckFixture(t)
	client, _ := githubWakeFixture(t, c)
	for n := range 30 {
		payload := fmt.Sprintf(`{"repository":{"full_name":"example/inbox"},"check_run":{"id":%d,"check_suite":{"head_branch":"foreign-branch"}}}`, n)
		githubDeliver(t, client, "check_run", fmt.Sprint(n), payload)
	}
	select {
	case <-c.fleet.wake:
		t.Fatal("unowned CI activity in an allowed repository woke supervision")
	case <-time.After(600 * time.Millisecond):
	}
}
