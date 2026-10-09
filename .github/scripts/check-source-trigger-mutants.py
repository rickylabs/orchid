#!/usr/bin/env python3
"""Compile source-comment trigger guard mutations, require assertion failures, restore bytes."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
# (label, file, exact anchor, mutated text)
mutants = [
    ('owner-id', 'source_binding.go', 'if cm.AuthorID != ownerGitHubID && !c.sourceGrantNamed(repo, cm.ID, cm.AuthorID) { // guard:source-owner', 'if false && cm.AuthorID != ownerGitHubID { // guard:source-owner'),
    ('grant-names-author', 'owner_native_grant_store.go', 'return r.CommentID == comment && r.AuthorID == author', 'return r.CommentID == comment'),
    ('owner-grant-owner-only', 'owner_native_grant_store.go', '\treturn author == ownerGitHubID\n}', '\treturn true\n}'),
    ('admission-comment-author', 'owner_native_grant_store.go', 'commentGrantNames(history.Request, int64(is.Number), sourceAuthor(is))', 'true'),
    ('policy-member-is-author', 'owner_native_grant_store.go', 'p.Member == r.AuthorID && ', ''),
    ('recheck-reads-current', 'source_binding.go', 'c.considerSourceComment(ctx, lookup, p.repo, cm, now, start)', 'c.considerSourceComment(ctx, lookup, p.repo, p.cm, now, start)'),
    ('first-line-exact', 'source_binding.go', 'if first != "/swarm" { // guard:source-first-line', 'if strings.TrimSpace(first) != "/swarm" { // guard:source-first-line'),
    ('bind-once', 'source_binding.go', 'if bound { // guard:source-once', 'if false && bound { // guard:source-once'),
    ('first-start', 'source_binding.go', 'if !started { // guard:source-first-start', 'if false && !started { // guard:source-first-start'),
    ('unedited', 'source_binding.go', 'if !cm.UpdatedAt.Equal(cm.CreatedAt) { // guard:source-unedited', 'if false && !cm.UpdatedAt.Equal(cm.CreatedAt) { // guard:source-unedited'),
    ('after-start', 'source_binding.go', 'if cm.CreatedAt.Before(start) { // guard:source-after-start', 'if false && cm.CreatedAt.Before(start) { // guard:source-after-start'),
    ('fresh', 'source_binding.go', 'if now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh', 'if false && now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh'),
    ('repo-match', 'source_binding.go', '} else if o.Repo != "" && !strings.EqualFold(o.Repo, repo) { // guard:source-repo-match', '} else if false { // guard:source-repo-match'),
    ('grant-required-at-bind', 'source_binding.go', '} else if !c.sourceGrantReady(repo, n, view.NodeID, body, key, cm.AuthorID) { // guard:source-grant-required', '} else if false && !c.sourceGrantReady(repo, n, view.NodeID, body, key, cm.AuthorID) { // guard:source-grant-required'),
    ('grant-required-at-admission', 'owner_native_grant_store.go', 'if matches == 0 && subject.comment { // guard:source-grant-admission', 'if false && matches == 0 && subject.comment { // guard:source-grant-admission'),
    ('still-open', 'source_binding.go', 'if live || !known { // guard:source-still-open', 'if true || live || !known { // guard:source-still-open'),
    ('cursor-past-newest', 'source_binding.go', 'next := scan.Newest.Add(-time.Second) // exclusive since', 'next := scan.Newest.Add(time.Second) // exclusive since'),
    ('scan-continue-exclusive', 'source_binding.go', 'if next := summary.Last.Add(-time.Second); next.After(scan.Since) { // guard:source-scan-advance', 'if next := summary.Last; next.After(scan.Since) { // guard:source-scan-advance'),
    ('reply-budget', 'source_binding.go', 'left-- // guard:source-reply-budget', '_ = left // guard:source-reply-budget'),
    ('admission-no-read', 'owner_native_grant_store.go', '\tif subject.comment {\n\t\t// The source feed admits', '\tif false {\n\t\t// The source feed admits'),
    ('admit-confirmed', 'source_binding.go', 'if known || job { // guard:source-admit-confirmed', 'if true || known || job { // guard:source-admit-confirmed'),
    ('finish-confirmed-only', 'source_binding.go', 'if done || (!all[key] && c.st.Jobs[key] == nil && !completing) { // guard:source-finish-confirmed', 'if done || (len(all) >= 0 && c.st.Jobs[key] == nil && !completing) { // guard:source-finish-confirmed'),
    ('reply-turns', 'source_binding.go', 'mem.replyTurn = turn + attempts // guard:source-reply-turns', 'mem.replyTurn = turn + 0*attempts // guard:source-reply-turns'),
    ('refusal-log', 'source_binding.go', 'log.Printf("source triggers: %s#%d comment %d refused (%s): %s", repo, n, cm.ID, refusal, matrixReasons[refusal].hint) // guard:source-refusal-log', '_ = fmt.Sprintf("source triggers: %s#%d comment %d refused (%s): %s", repo, n, cm.ID, refusal, matrixReasons[refusal].hint) // guard:source-refusal-log'),
    ('scan-not-persisted', 'source_binding.go', '\t\tc.st.SourceScans[repo] = scan\n', '\t\tdelete(c.st.SourceScans, repo)\n'),
    ('repo-turns', 'source_binding.go', 'mem.repoTurn = turn + scanned // guard:source-repo-turns', 'mem.repoTurn = turn + 1 + 0*scanned // guard:source-repo-turns'),
    ('check-turns', 'source_binding.go', 'mem.checkTurn = turn + checkedKeys', 'mem.checkTurn = turn + 1 + 0*checkedKeys'),
    ('check-budget', 'source_binding.go', 'if budget >= 2 { // guard:source-check-budget', 'if true || budget >= 2 { // guard:source-check-budget'),
    ('bind-budget', 'source_binding.go', 'if budget <= 0 { // guard:source-bind-budget', 'if false && budget <= 0 { // guard:source-bind-budget'),
    ('rate-floor', 'source_binding.go', 'if meta.Remaining < 0 || meta.Remaining >= sourceRateFloor { // guard:source-rate-floor', 'if true || meta.Remaining < 0 || meta.Remaining >= sourceRateFloor { // guard:source-rate-floor'),
    ('reply-dropped-on-failure', 'source_binding.go', '"source triggers: reply on %s#%d failed; it stays queued", repo, n)\n', '"source triggers: reply on %s#%d failed; it stays queued", repo, n)\n\t\t\tc.st.mu.Lock()\n\t\t\tb.Outbox = b.Outbox[1:]\n\t\t\tc.st.mu.Unlock()\n'),
    ('blocked-not-terminal', 'source_binding.go', 'return state == "done" || state == "stopped"\n', 'return state == "done" || state == "stopped" || state == "blocked"\n'),
    ('binding-repo', 'main.go', 'if o.Repo != "" && !strings.EqualFold(o.Repo, is.Source.Repo) { // guard:source-binding-repo', 'if false { // guard:source-binding-repo'),
    ('grant-claim-once', 'owner_native_grant_store.go', 'if subject.comment && !s.claimLocked(intent.OperationID, is.Number) { // guard:grant-claim-once', 'if false { // guard:grant-claim-once'),
    ('comment-key-space', 'owner_native_grant_store.go', 'return shaText([]byte("comment\\x00" + strings.ToLower(subject.repo) + "\\x00" + strconv.Itoa(subject.number)))', 'return s.issueKey(subject.number)'),
    ('binding-digest', 'matrix.go', '\tif is.Source != nil {\n\t\t// A comment binding', '\tif false {\n\t\t// A comment binding'),
    ('pr-needs-marker', 'source_binding.go', 'if strings.Contains(cm.HTMLURL, "/pull/") && !marked { // guard:source-pr-needs-marker', 'if false { // guard:source-pr-needs-marker'),
    ('pr-marked-accepted', 'source_binding.go', 'if strings.Contains(cm.HTMLURL, "/pull/") && !marked { // guard:source-pr-needs-marker', 'if strings.Contains(cm.HTMLURL, "/pull/") { // guard:source-pr-needs-marker'),
    ('pr-view-needs-marker', 'source_binding.go', 'if view.PR && !marked { // guard:source-pr-view-needs-marker', 'if false { // guard:source-pr-view-needs-marker'),
    ('pr-marker-exact', 'source_binding.go', 'if last < 0 || !cockpitLaunchMarker.MatchString(lines[last]) { // guard:source-pr-marker-exact', 'if last < 0 || !cockpitLaunchMarker.MatchString(strings.TrimRight(lines[last], " \\t")) { // guard:source-pr-marker-exact'),
    ('pr-marker-unfenced', 'source_binding.go', 'return fence == "" // guard:source-pr-marker-unfenced', 'return fence == "" || true // guard:source-pr-marker-unfenced'),
    ('ignored-bounded', 'source_binding.go', '\t\t\tsaturated = !mem.ignoredFull\n\t\t\tmem.ignoredFull = true\n\t\t\tseen = true\n', '\t\t\tmem.ignored = map[int64]bool{cm.ID: true}\n'),
    ('ignored-once', 'source_binding.go', 'if !seen { // guard:source-ignored-once', 'if true { // guard:source-ignored-once'),
    ('grant-target-trigger-label', 'owner_native_grant_store.go', 'is.Labels = append(append([]string{}, labels...), ownerNativeTrigger) // guard:grant-target-trigger-label', 'is.Labels = append([]string{}, labels...) // guard:grant-target-trigger-label'),
    ('grant-target-exact', 'owner_native_grant_store.go', 'return reason == "" && !tgt.Disabled && tgt.Repo == repo // guard:grant-target-resolves', 'return reason == "" && !tgt.Disabled // guard:grant-target-resolves'),
    ('grant-target-enabled', 'owner_native_grant_store.go', 'return reason == "" && !tgt.Disabled && tgt.Repo == repo // guard:grant-target-resolves', 'return reason == "" && tgt.Repo == repo // guard:grant-target-resolves'),
]
tests = '^Test(Source|Policy|OwnerGrantNever|OwnerNativeCommentGrant|CockpitLaunchMarker|OwnerNative(InboxGrant|RepoLess|RepoKey|NoTargetLabel|TargetMatches))'
files = sorted({m[1] for m in mutants})
paths = {name: root / 'cmd/divybot' / name for name in files}
originals = {name: path.read_bytes() for name, path in paths.items()}
results = []
try:
    with tempfile.TemporaryDirectory(prefix='source-trigger-mutants-') as temp:
        binary = Path(temp) / 'source.test'
        for label, name, before, after in mutants:
            source = originals[name].decode()
            assert source.count(before) == 1, label + ' anchor drifted'
            paths[name].write_text(source.replace(before, after))
            compiled = subprocess.run(['go', 'test', '-c', '-o', str(binary), './cmd/divybot'], cwd=root, capture_output=True, text=True)
            assert compiled.returncode == 0, label + ' failed compilation: ' + compiled.stderr
            tested = subprocess.run([str(binary), '-test.run=' + tests, '-test.count=1'], cwd=root / 'cmd/divybot', capture_output=True, text=True, timeout=300)
            output = tested.stdout + tested.stderr
            red = tested.returncode != 0 and '--- FAIL: Test' in output and 'panic:' not in output
            result = {'mutation': label, 'compileExitCode': compiled.returncode, 'testExitCode': tested.returncode, 'compiledAssertionRed': red}
            results.append(result)
            print(json.dumps(result), flush=True)
            if not red:
                print(output, flush=True)
            paths[name].write_bytes(originals[name])
            assert red, label + ' did not fail a behavioral assertion'
finally:
    for name, path in paths.items():
        path.write_bytes(originals[name])
    restored = all(paths[name].read_bytes() == content for name, content in originals.items())
    print(json.dumps({'exactSourceRestored': restored, 'sourceSHA256': {name: hashlib.sha256(value).hexdigest() for name, value in originals.items()}, 'results': results}), flush=True)
    assert restored
