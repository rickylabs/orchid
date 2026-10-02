package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const finalCommentBodyFile = ".divybot-final-comment.sh"

// The worker gets a body constructor bound to this launch, not a private run ID
// to copy into a public comment. Only public opaque identifiers enter the script.
func finalCommentBodyScript(key string) (string, error) {
	marker := finalCommentMarker(key)
	if marker == "" {
		return "", errors.New("final_comment_identity_invalid")
	}
	return fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$#" -ne 0 ]; then
  printf 'final-comment-input-invalid\n' >&2
  exit 2
fi
if ! LC_ALL=C awk '
BEGIN { marker = "%s" }
{
  if (length(body) + length($0) + 1 > 60000 || index($0, "<!-- orchid-run")) {
    invalid = 1
    exit
  }
  if ($0 ~ /[^[:space:]]/) nonblank = 1
  body = body $0 "\n"
}
END {
  if (invalid || !nonblank) exit 2
  printf "%%s\n%%s\n", body, marker
}' ; then
  printf 'final-comment-input-invalid\n' >&2
  exit 2
fi
`, marker), nil
}

// Staging and exclusion both precede registration, including interactive agents
// whose goal is delivered directly rather than read from a staged goal file.
func (h Host) stageFinalCommentBody(ctx context.Context, workdir, key string) error {
	script, err := finalCommentBodyScript(key)
	if err != nil {
		return err
	}
	exclude := filepath.Join(workdir, ".git", "info", "exclude")
	prep := fmt.Sprintf("set -e\ncommand -v awk >/dev/null\ncd %s\ntest ! -L %s\nmarker_tracked=$(git ls-files -- %s)\ntest -z \"$marker_tracked\"\ngrep -qxF %s %s || printf '%%s\\n' %s >> %s\n",
		shq(workdir), shq(finalCommentBodyFile), shq(finalCommentBodyFile),
		shq(finalCommentBodyFile), shq(exclude), shq(finalCommentBodyFile), shq(exclude))
	if _, err := h.runRemote(ctx, prep); err != nil {
		return errors.New("final_comment_preparation_failed")
	}
	if err := h.writeFile(ctx, filepath.Join(workdir, finalCommentBodyFile), script); err != nil {
		return errors.New("final_comment_preparation_failed")
	}
	return nil
}

// Claude and OpenCode receive their goal directly; the existing goal artifact
// remains available to staged consumers. All get the launch-bound constructor.
func (h Host) stageWorkerGoal(ctx context.Context, workdir, key, goal string, stagedGoal bool) error {
	if strings.TrimSpace(goal) == "" {
		return errors.New("worker_goal_empty")
	}
	if err := h.stageFinalCommentBody(ctx, workdir, key); err != nil {
		return err
	}
	if !stagedGoal {
		return nil
	}
	goalFile := filepath.Join(workdir, ".divybot-goal.md")
	if err := h.writeFile(ctx, goalFile, goal); err != nil {
		return err
	}
	_, _ = h.runRemote(ctx, fmt.Sprintf("grep -qxF .divybot-goal.md %s/.git/info/exclude 2>/dev/null || echo .divybot-goal.md >> %s/.git/info/exclude", shq(workdir), shq(workdir)))
	return nil
}

func finalCommentBodyInstruction(key string) string {
	instruction := finalCommentInstruction(key)
	if instruction == "" {
		return ""
	}
	return instruction + "\nFor the requested final comment, prepare the visible report in an unmarked file, then build its body with `sh .divybot-final-comment.sh < REPORT.md > FINAL-COMMENT.md`. Post that file using `gh issue comment <issue> --repo <repository> --body-file FINAL-COMMENT.md` at the destination specified by the brief. Build successfully before posting; never post a partial or empty output after an error. Use this helper only for the requested final comment, not progress or review messages. The helper adds identity only; the visible report must still follow the brief's format and privacy rules.\n"
}
