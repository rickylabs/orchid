package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

// This is an issue-level observation. It can exist before there is a seat or a
// dispatch binding. The filename and dispatch ID contain hashes only; neither
// the issue body nor a native identity enters the reader-facing record.
type launchStateRecord struct {
	SchemaVersion int           `json:"schemaVersion"`
	Issue         dispatchIssue `json:"issue"`
	DispatchID    string        `json:"dispatchId"`
	State         string        `json:"state"`
	ReasonCode    *string       `json:"reasonCode"`
	ObservedAt    string        `json:"observedAt"`
}

func launchStatePath(root, inbox string, is Issue) string {
	return filepath.Join(root, "launch-"+shaText([]byte(inbox+"\x00"+is.ID))+".json")
}

func launchStateDispatchID(inbox string, is Issue) string {
	return "assignment_" + shaText([]byte("launch-refusal\x00"+inbox+"\x00"+is.ID+"\x00"+briefDigest(is)))
}

func publishLaunchState(root string, owner *receiptOwner, inbox string, n int, is Issue, state, reason string) error {
	if !privateReceiptRoot(root) || !repositoryName.MatchString(inbox) || is.ID == "" || n < 1 || is.Number != n {
		return errMatrix
	}
	if state != "refused" && state != "launching" && state != "launched" && state != "blocked" {
		return errMatrix
	}
	if state == "refused" {
		if !validMatrixRefusal(matrixRefusal{Status: "refused", ReasonCode: reason}) || reason == "launch-failed" {
			return errMatrix
		}
	} else if state == "blocked" {
		if reason != "goal-prompt-unconfirmed" {
			return errMatrix
		}
	} else if reason != "" {
		return errMatrix
	}
	var reasonCode *string
	version := 1
	if state == "refused" || state == "blocked" {
		reasonCode = &reason
	}
	if state == "blocked" {
		version = 2
	}
	record := launchStateRecord{SchemaVersion: version, Issue: dispatchIssue{Repo: inbox, Number: n},
		DispatchID: launchStateDispatchID(inbox, is), State: state, ReasonCode: reasonCode,
		ObservedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
	path := launchStatePath(root, inbox, is)
	// Keep the first observation time across repeat polls of the same attempt.
	if stat, err := os.Lstat(path); err == nil {
		if !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
			return errMatrix
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return errMatrix
		}
		var prior launchStateRecord
		if json.Unmarshal(raw, &prior) == nil && prior.SchemaVersion == record.SchemaVersion &&
			prior.Issue == record.Issue && prior.DispatchID == record.DispatchID &&
			prior.State == state && ((prior.ReasonCode == nil && reasonCode == nil) ||
			(prior.ReasonCode != nil && reasonCode != nil && *prior.ReasonCode == *reasonCode)) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errMatrix
	}
	body, err := json.Marshal(record)
	if err != nil {
		return errMatrix
	}
	f, err := os.CreateTemp(root, ".launch-")
	if err != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return errMatrix
	}
	if err = transferReceiptOwner(owner, f.Name()); err != nil {
		f.Close()
		return errMatrix
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return errMatrix
	}
	if err = f.Close(); err != nil {
		return errMatrix
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errMatrix
	}
	return syncDirectory(root)
}

func (c *Coord) publishLaunchState(n int, is Issue, state, reason string) {
	if c.dry {
		return
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err == nil {
		// A comment binding's observation belongs to its source issue.
		home := c.issueHome(n)
		is.Number = home.Number
		err = publishLaunchState(c.cfg.Matrix.ReceiptRoot, owner, home.Repo, home.Number, is, state, reason)
	}
	if err != nil {
		log.Printf("issue #%d: launch state unavailable", n)
	}
}
