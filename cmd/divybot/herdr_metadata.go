package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Presentation comes only from the already-dispatched binding. These tokens are
// never read back for identity, lifecycle, delivery, or completion decisions.
func (h Host) publishDisplayMetadata(ctx context.Context, d *dispatchBinding, task string) error {
	if d == nil || d.State != "dispatched" || d.Host != h.Name || d.Location == nil || d.Location.PaneID == "" || d.Location.WorkspaceID == "" || d.Issue.Number <= 0 {
		return errors.New("display binding unavailable")
	}
	label := []rune(strings.Join(strings.Fields(task), " "))
	if len(label) > 64 {
		label = label[:64]
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := h.herdr(check, "pane", "report-metadata", d.Location.PaneID, "--source", "orchid:dispatch",
		"--token", "issue=#"+strconv.Itoa(d.Issue.Number), "--token", "model="+d.Model,
		"--token", "role="+d.Role, "--token", "task="+string(label))
	if err != nil {
		return errors.New("display metadata unavailable")
	}
	_, err = herdrUnwrap(out)
	return err
}
