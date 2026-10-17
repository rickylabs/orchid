package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type savedHerdrMachine struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Target  string `json:"target"`
	Session string `json:"session"`
	Enabled bool   `json:"enabled"`
}

// The CLI catalog is authoritative. Resolve labels to opaque IDs, and refuse
// account/session drift before any command. Never derive a profile from a host
// name or route a missing/disconnected profile to Local.
func (h Host) savedHerdrMachine(ctx context.Context) (savedHerdrMachine, error) {
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := run(check, "herdr", "machine", "list", "--json")
	var profiles []savedHerdrMachine
	if err != nil || json.Unmarshal([]byte(out), &profiles) != nil {
		return savedHerdrMachine{}, errors.New("herdr machine catalog unavailable")
	}
	session := h.HerdrSession
	if session == "" {
		session = "default"
	}
	var found *savedHerdrMachine
	// Match CLI precedence: exact ID first, then a unique label.
	for _, profile := range profiles {
		if profile.ID == h.HerdrMachine {
			copy := profile
			found = &copy
			break
		}
	}
	if found == nil {
		for _, profile := range profiles {
			if profile.Label == h.HerdrMachine {
				if found != nil {
					return savedHerdrMachine{}, errors.New("herdr machine ambiguous")
				}
				copy := profile
				found = &copy
			}
		}
	}
	if found == nil || found.ID == "" || !found.Enabled || h.SSH == "" || found.Target != h.SSH || found.Session != session {
		return savedHerdrMachine{}, errors.New("herdr machine binding unavailable")
	}
	return *found, nil
}

// herdrSessionArgs selects the host's Herdr server session for a command run
// in the target account; none is the default session.
func (h Host) herdrSessionArgs(args ...string) []string {
	if h.HerdrSession == "" {
		return args
	}
	return append([]string{"--session", h.HerdrSession}, args...)
}

func (h Host) herdr(ctx context.Context, args ...string) (string, error) {
	if h.HerdrMachine == "" {
		return h.herdrAccountCommand(ctx, h.herdrSessionArgs(args...)...)
	}
	profile, err := h.savedHerdrMachine(ctx)
	if err != nil {
		return "", err
	}
	check, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := run(check, "herdr", append([]string{"--machine", profile.ID}, args...)...)
	if err != nil {
		return "", errors.New("herdr machine command unavailable")
	}
	return out, nil
}
