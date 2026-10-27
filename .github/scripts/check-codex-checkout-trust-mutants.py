#!/usr/bin/env python3
"""Compile checkout-trust guard mutants in an isolated copy; require assertion-red."""
from pathlib import Path
import json
import shutil
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
# (name, source, exact anchor, replacement)
mutants = [
    ('invalid-checkout-skip', 'checkout_storage.go',
     'return nil // preserve the existing belt-and-suspenders skip',
     'return goalError("checkout-root-invalid") // removed skip'),
    ('cleanup-only-trust-is-fatal', 'main.go',
     'if errors.Is(cleanupErr, errCodexTrustRemoval) { // guard:trust-only-blocks-cleanup',
     'if true { // guard:trust-only-blocks-cleanup'),
    ('startup-recovery', 'main.go',
     'c.reconcileCheckoutTrust(ctx) // guard:trust-startup-reconcile',
     '_ = ctx // guard:trust-startup-reconcile'),
    ('tick-recovery', 'main.go',
     'c.reconcileCheckoutTrust(ctx) // guard:trust-tick-reconcile',
     '_ = ctx // guard:trust-tick-reconcile'),
    ('bounded-release-revokes', 'main.go',
     'if err := host.revokeJobCheckoutTrust(cleanup, j); err != nil { // guard:trust-bound-release',
     'if err := host.revokeJobCheckoutTrust(cleanup, &Job{Agent: "claude"}); err != nil { // guard:trust-bound-release'),
    ('grant-validates-checkout', 'remote_control_transport.go',
     'if !validTrustCheckout(run.Cwd) { // guard:trust-grant-checkout',
     'if false { // guard:trust-grant-checkout'),
    ('typed-write-validates-checkout', 'codex_checkout_trust.go',
     'if !validTrustCheckout(w.cwd) || w.KeyPath != codexCheckoutTrustKey(w.cwd)',
     'if !validRemoteCwd(w.cwd) || w.KeyPath != codexCheckoutTrustKey(w.cwd)'),
    ('positive-issue', 'checkout_storage.go',
     'return n > 0 && validRemoteCwd(path)',
     'return validRemoteCwd(path)'),
    ('exact-root-boundary', 'codex_checkout_trust.go',
     'if filepath.Dir(cwd) == root { // guard:trust-direct-checkout',
     'if strings.HasPrefix(cwd, root) { // guard:trust-direct-checkout'),
    ('live-job-retained', 'codex_checkout_trust.go',
     'if !validTrustCheckout(cwd) || retained[cwd] {',
     'if !validTrustCheckout(cwd) {'),
    ('released-job-not-retained', 'codex_checkout_trust.go',
     'if j.stopUnconfirmed() {',
     'if false {'),
    ('native-orphan-removal', 'codex_checkout_trust.go',
     'if err := p.setCheckoutTrust(cwd, false); err != nil { // guard:trust-orphan-removal',
     'if err := error(nil); err != nil { // guard:trust-orphan-removal'),
    ('unsaved-job-recovery', 'codex_checkout_trust.go',
     'raw, err := p.request(codexConfigRead, codexCheckoutTrustRead{})',
     'if len(retained) == 0 { return nil }; raw, err := p.request(codexConfigRead, codexCheckoutTrustRead{})'),
    ('untrusted-project-preserved', 'codex_checkout_trust.go',
     'if project.TrustLevel != codexTrustedCheckout {',
     'if false {'),
    ('revoke-before-directory-removal', 'checkout_storage.go',
     'if err := h.revokeJobCheckoutTrust(ctx, j); err != nil {',
     'if err := error(nil); err != nil {'),
    ('failed-revoke-reported', 'codex_checkout_trust.go',
     'return failure\n}',
     '_ = failure; return nil\n}'),
]
tests = '^Test(CodexTrust|CodexCheckout|CheckoutCleanup|TrustCheckout)'
results = []
with tempfile.TemporaryDirectory(prefix='orchid-trust-mutants-') as temp:
    copy = Path(temp) / 'source'
    copy.mkdir()
    shutil.copy2(root / 'go.mod', copy / 'go.mod')
    shutil.copytree(root / 'cmd', copy / 'cmd')
    package = copy / 'cmd/divybot'
    originals = {name: (package / name).read_bytes() for name in {row[1] for row in mutants}}
    binary = Path(temp) / 'trust.test'

    def compile_and_run():
        built = subprocess.run(['go', 'test', '-c', '-o', str(binary), './cmd/divybot'],
                               cwd=copy, capture_output=True, text=True, timeout=120)
        assert built.returncode == 0, 'compile failed: ' + built.stderr
        ran = subprocess.run([str(binary), '-test.run=' + tests, '-test.count=1', '-test.timeout=30s'],
                             cwd=package, capture_output=True, text=True, timeout=40)
        return ran

    control = compile_and_run()
    assert control.returncode == 0, 'positive control failed: ' + control.stdout + control.stderr
    print(json.dumps({'positiveControl': 'PASS'}), flush=True)
    for label, name, before, after in mutants:
        source = originals[name].decode()
        assert source.count(before) == 1, label + ': anchor drift'
        try:
            (package / name).write_text(source.replace(before, after))
            tested = compile_and_run()
            output = tested.stdout + tested.stderr
            red = tested.returncode != 0 and '--- FAIL: Test' in output and 'panic:' not in output
            result = {'mutation': label, 'compiledAssertionRed': red}
            results.append(result)
            print(json.dumps(result), flush=True)
            assert red, label + ' did not fail a behavioral assertion: ' + output
        finally:
            (package / name).write_bytes(originals[name])
    assert all((package / name).read_bytes() == body for name, body in originals.items())
    print(json.dumps({'isolatedSourcesRestored': True, 'mutants': len(results)}), flush=True)
