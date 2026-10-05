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
    ('owner-id', 'source_binding.go', 'if cm.AuthorID != ownerGitHubID { // guard:source-owner', 'if false && cm.AuthorID != ownerGitHubID { // guard:source-owner'),
    ('first-line', 'source_binding.go', 'if strings.TrimSpace(first) != "/swarm" { // guard:source-first-line', 'if strings.TrimSpace(first) != "/swarm" && !strings.Contains(body, "\\n/swarm") { // guard:source-first-line'),
    ('bind-once', 'source_binding.go', 'if bound { // guard:source-once', 'if false && bound { // guard:source-once'),
    ('first-start', 'source_binding.go', 'if !started { // guard:source-first-start', 'if false && !started { // guard:source-first-start'),
    ('cursor-advance', 'source_binding.go', 'complete && next.After(cursor) { // guard:source-cursor-advance', '(complete || true) && next.After(cursor) { // guard:source-cursor-advance'),
    ('fresh', 'source_binding.go', 'if now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh', 'if false && now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh'),
    ('repo-match', 'source_binding.go', '} else if o.Repo != "" && !strings.EqualFold(o.Repo, repo) { // guard:source-repo-match', '} else if false { // guard:source-repo-match'),
    ('still-open', 'source_binding.go', 'if !exists || view.State != "OPEN" || view.NodeID != b.IssueID { // guard:source-still-open', 'if !exists || false && (view.State != "OPEN" || view.NodeID != b.IssueID) { // guard:source-still-open'),
    ('blocked-not-terminal', 'source_binding.go', 'return state == "done" || state == "stopped"\n', 'return state == "done" || state == "stopped" || state == "blocked"\n'),
    ('binding-repo', 'main.go', 'if o.Repo != "" && !strings.EqualFold(o.Repo, is.Source.Repo) { // guard:source-binding-repo', 'if false { // guard:source-binding-repo'),
    ('grant-claim-once', 'owner_native_grant_store.go', 'if subject.comment && !s.claimLocked(intent.OperationID, is.Number) { // guard:grant-claim-once', 'if false { // guard:grant-claim-once'),
    ('comment-key-space', 'owner_native_grant_store.go', 'return shaText([]byte("comment\\x00" + strings.ToLower(subject.repo) + "\\x00" + strconv.Itoa(subject.number)))', 'return s.issueKey(subject.number)'),
    ('binding-digest', 'matrix.go', '\tif is.Source != nil {\n\t\t// A comment binding', '\tif false {\n\t\t// A comment binding'),
]
tests = '^Test(Source|OwnerNativeCommentGrant)'
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
