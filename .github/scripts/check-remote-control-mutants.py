#!/usr/bin/env python3
"""Compiled Remote Control guard mutations. Every mutation must fail an assertion."""
import argparse
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]

def replace(code, old, new):
    if code.count(old) != 1:
        raise RuntimeError('mutation anchor must be unique')
    return code.replace(old, new, 1)

def function_replace(code, function, old, new):
    start = code.index(function)
    end = code.find('\nfunc ', start + len(function))
    if end < 0:
        end = len(code)
    return code[:start] + replace(code[start:end], old, new) + code[end:]

# File, function (or whole file), exact anchor, replacement, focused control.
MUTANTS = [
    ('endpoint-raw-component-recheck', 'remote_control_transport.go', None, "if snapshot!=dir_stamp(os.lstat(name)):refuse('remote-control-endpoint-changed')", 'if False:pass', 'TestRemoteControlRawComponentRecheckedAfterConnect'),
    ('endpoint-raw-components', 'remote_control_transport.go', None, 'path=link if os.path.isabs(link) else os.path.join(directory,link)', 'path=os.path.abspath(link if os.path.isabs(link) else os.path.join(directory,link))', 'TestRemoteControlCanonicalAliasPreservesRawComponents'),
    ('endpoint-component-directory-reason', 'remote_control_transport.go', None, "except NotADirectoryError:refuse('remote-control-endpoint-parent-unsafe')", 'except NotADirectoryError:raise ValueError()', 'TestRemoteControlCanonicalAliasPreservesRawComponents/file-dotdot'),
    ('endpoint-link-owner', 'remote_control_transport.go', None, 'entry_before.st_uid!=uid', 'entry_before.st_uid<0', 'TestRemoteControlCanonicalEndpoint/foreign-link'),
    ('endpoint-target-owner', 'remote_control_transport.go', None, 'if before.st_uid!=uid:', 'if before.st_uid<0:', 'TestRemoteControlCanonicalEndpoint/foreign-target'),
    ('endpoint-target-mode', 'remote_control_transport.go', None, 'stat.S_IMODE(before.st_mode)!=0o600', 'False', 'TestRemoteControlCanonicalEndpoint/socket-0660'),
    ('endpoint-recursive-link', 'remote_control_transport.go', None, 'path=link if os.path.isabs(link) else os.path.join(directory,link)', 'path=os.path.realpath(link if os.path.isabs(link) else os.path.join(directory,link))', 'TestRemoteControlCanonicalEndpoint/chain'),
    ('endpoint-readlink-once', 'remote_control_transport.go', None, 'link=os.readlink(entry)', 'link=os.readlink(entry);link=os.readlink(entry)', 'TestRemoteControlCanonicalEndpoint/single-read'),
    ('endpoint-dial-resolved', 'remote_control_transport.go', None, 's.connect(path)', 's.connect(entry)', 'TestRemoteControlCanonicalEndpoint/dial-resolved'),
    ('endpoint-socket-type', 'remote_control_transport.go', None, 'not stat.S_ISSOCK(before.st_mode)', 'False', 'TestRemoteControlCanonicalEndpoint/non-socket'),
    ('endpoint-dangling-reason', 'remote_control_transport.go', None, "except FileNotFoundError:refuse('remote-control-endpoint-dangling')", 'except FileNotFoundError:raise ValueError()', 'TestRemoteControlCanonicalEndpoint/dangling'),
    ('endpoint-parent-write', 'remote_control_transport.go', None, 'stat.S_IMODE(p.st_mode)&0o022', 'False', 'TestRemoteControlCanonicalEndpoint/parent-writable'),
    ('endpoint-parent-chain', 'remote_control_transport.go', None, 'stat.S_ISLNK(p.st_mode)', 'False', 'TestRemoteControlCanonicalEndpoint/ancestor-link'),
    ('endpoint-entry-recheck', 'remote_control_transport.go', None, 'stamp(entry_before)!=stamp(os.lstat(entry))', 'False', 'TestRemoteControlCanonicalEndpoint/entry-replaced'),
    ('endpoint-target-recheck', 'remote_control_transport.go', None, 'stamp(before)!=stamp(os.lstat(path))', 'False', 'TestRemoteControlCanonicalEndpoint/target-replaced'),
    ('endpoint-canonical-directory-recheck', 'remote_control_transport.go', None, 'dir_stamp(d)!=dir_stamp(os.lstat(directory))', 'False', 'TestRemoteControlCanonicalEndpoint/canonical-directory-mode-changed'),
    ('endpoint-parent-recheck', 'remote_control_transport.go', None, "if dir_stamp(p)!=dir_stamp(os.fstat(parent_fd)):refuse('remote-control-endpoint-changed')\n  for name,snapshot in parents:\n   if snapshot!=dir_stamp(os.lstat(name)):refuse('remote-control-endpoint-changed')", 'pass', 'TestRemoteControlCanonicalEndpoint/parent-mode-changed'),
    ('endpoint-closed-diagnostic', 'remote_control_transport.go', 'func (d *remoteBridgeDiagnostic) reason', 'return nil\n}', 'return goalError(string(d.body))\n}', 'TestRemoteControlEndpointDiagnostics/raw'),
    ('endpoint-bounded-diagnostic', 'remote_control_transport.go', 'func (d *remoteBridgeDiagnostic) Write', 'b = b[:left]', 'b = b[:]', 'TestRemoteControlEndpointDiagnostics/overflow-whole'),
    ('peer-owner', 'remote_control_transport.go', None, "struct.unpack('3i',s.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[1]!=uid", "struct.unpack('3i',s.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[1]<0", 'TestRemoteControlForeignOwnerInputs/peer'),
    ('socket-owner', 'remote_control_transport.go', None, 'if before.st_uid!=uid:', 'if before.st_uid<0:', 'TestRemoteControlForeignOwnerInputs/socket-owner'),
    ('directory-owner', 'remote_control_transport.go', None, 'd.st_uid!=uid', 'd.st_uid<0', 'TestRemoteControlForeignOwnerInputs/directory-owner'),
    ('connection', 'remote_control_transport.go', 'func (p *goalRPC) remoteConnected', 'status.Status != "connected"', 'false', 'TestRemoteControlPreparationRequiresConnectionAndReadback/absent'),
    ('resume-id', 'remote_control_transport.go', 'func decodeRemoteThread', '(!newThread && v.Thread.ID != r.NativeSessionID)', 'false', 'TestRemoteControlPreparationRequiresConnectionAndReadback/wrong-resume-id'),
    ('name-readback', 'remote_control_transport.go', 'func (p *goalRPC) verifyRemoteThread', '*v.Thread.Name != r.Name', 'false', 'TestRemoteControlPreparationRequiresConnectionAndReadback/wrong-name'),
    ('trust-scope', 'remote_control_transport.go', 'func remoteThreadParams', 'r.Cwd: map[string]string{"trust_level": "trusted"}', 'r.Cwd: map[string]string{"trust_level": "trusted"}, "foreign": map[string]string{"trust_level": "trusted"}', 'TestRemoteControlScopedTrustAndExplicitDaemon'),
    ('rpc-thread-scope', 'native_goal_rpc.go', 'func (p *goalRPC) request', 'scope.ThreadID != p.thread', 'false', 'TestRemoteControlRPCScopes'),
    ('late-hook', 'native_goal.go', 'func acquireNativeGoalBinding', 'publishNativeIdentity(ctx, id, r.writeNativeIdentity)', 'publishNativeIdentity(context.Background(), id, r.writeNativeIdentity)', 'TestRemoteControlPostPromptHookCannotReplacePreparedThread/late'),
    ('consent', 'native_prompt.go', 'func deliverCodexPrompt', 'if d.noConsent {', 'if false {', 'TestRemoteControlNeverAnswersTrustConsent'),
    ('claude-proof-native-only', 'remote_control.go', 'func (h Host) remoteProof', 'return true, nil // guard:claude-proof-native-only', 'screen, err := h.visiblePromptScreen(ctx, location.PaneID); return err == nil && strings.Contains(screen, "/rc active"), err // guard:claude-proof-native-only', 'TestRemoteControlClaudeSpawnGate/absent'),
    ('claude-never-connected', 'remote_control.go', 'func writeRemoteObservation', '(kind == "claude" && state == "connected")', '(kind == "claude" && state == "connected" && false)', 'TestObservationLinkForm'),
    ('claude-link-revoke-published', 'native_claude_bridge.go', 'func (s *claudeLinkStore) withholdLocked', 'if s.published[key] {', 'if s.published[key] && false {', 'TestClaudeLinkRevokedFromPublishedRow/record-removed'),
    ('claude-link-sources-agree', 'native_claude_bridge.go', 'func (h Host) readClaudeBridge', 'case next.url != url:', 'case next.url != url && false:', 'TestObservationCarriesNativeClaudeLink/sources-disagree'),
    ('claude-link-launched-only', 'native_claude_bridge.go', 'func (h Host) readClaudeBridge', 'case read.PID != launched.PID || read.Start != launched.Start:', 'case false && (read.PID != launched.PID || read.Start != launched.Start):', 'TestAuditO89FirstBridgeLessProcessCannotBeReplaced'),
    ('claude-link-alive-now', 'native_claude_bridge.go', 'func (s *claudeLinkStore) publish', 'live := candidate != nil && alive()', 'live := candidate != nil', 'TestAuditO89EndedPIDWhileRefreshPending'),
    ('footer-thread', 'remote_control_identity.go', 'func verifyCodexFooterAttachment', 'id != run.NativeSessionID', 'false', 'TestRemoteControlContinuousFooterIdentity/foreign'),
    ('footer-deadline', 'remote_control_identity.go', 'func verifyCodexFooterAttachment', ' || ctx.Err() != nil', '', 'TestRemoteControlContinuousFooterIdentity/late'),
    ('ephemeral-descendants', 'remote_control_stop.go', 'func (p *goalRPC) remoteChildren', 'loaded, err := p.remoteLoadedChildren()', 'loaded, err := []nativeRemoteChild{}, error(nil)', 'TestRemoteControlLoadedDescendants/active-ephemeral'),
    ('archived-descendants', 'remote_control_stop.go', 'func (p *goalRPC) remoteChildren', '[]bool{false, true}', '[]bool{false}', 'TestRemoteControlArchivedChildNeverProvesShutdown'),
    ('streaming', 'remote_control_stop.go', 'func (p *goalRPC) remoteThreadIdle', 'case "completed", "interrupted", "failed":', 'case "completed", "interrupted", "failed", "inProgress":', 'TestRemoteControlNativeWorkQuiescence/streaming'),
    ('queue', 'remote_control_stop.go', 'func (p *goalRPC) remoteThreadIdle', 'len(page.Data) != 0', 'len(page.Data) > 1', 'TestRemoteControlNativeWorkQuiescence/queue'),
    ('terminate-readback', 'remote_control_stop.go', 'func (p *goalRPC) stopRemoteThread', '!result.Terminated', 'false', 'TestRemoteControlNativeStopReadbackAndDeadline/terminate-false'),
    ('native-turn-notice', 'remote_control_native_turn.go', 'func (p *goalRPC) scopedNativeTurnAgrees', 'return p.turnEvents[i].ID == id && p.turnEvents[i].Status == status', 'return true', 'TestRemoteControlNativeTurnSignals/other-turn'),
    ('native-final', 'native_goal_rpc.go', 'func (p *goalRPC) lastTurnCompleted', 'final := false', 'final := true', 'TestRemoteControlNativeTurnSignals/no-final'),
    ('capacity', 'main.go', 'func occupiesAdmissionSlot', 'if j != nil && j.RemoteControl != nil {', 'if false {', 'TestRemoteControlCapacityHeldUntilRemoval'),
    ('goal-binding-deadline', 'remote_control_goal_binding.go', 'func bindRemoteNativeGoal', 'err != nil || ctx.Err() != nil ||', 'err != nil ||', 'TestRemoteControlPreparedGoalBindingRechecks/late'),
    ('goal-native-binding', 'remote_control_goal_binding.go', 'func bindRemoteNativeGoal', 'id != j.RemoteControl.NativeSessionID', 'false', 'TestRemoteControlPreparedGoalBindingRechecks/wrong-private-id'),
    ('teardown-occupant', 'remote_control_cleanup.go', 'func (c *Coord) remoteTeardownOccupant', 'return c.checkRemoteHook(ctx, h, j)', 'return true', 'TestRemoteControlTeardownNeedsAttachedIdentity'),
    ('final-completion-lifecycle', 'completion.go', 'func (c *Coord) completionEvidence', 'e = p.reconcileNativeLifecycle()', 'e = nil', 'TestRemoteControlLateNativeTurnCannotCertifyCompletion/late-own'),
    ('final-stop-goal-lifecycle', 'remote_control_stop.go', 'func (p *goalRPC) stopRemoteThread', 'return p.reconcileNativeLifecycle()', 'return nil', 'TestRemoteControlStopThreadFinalGoalNotice/late-own'),
    ('final-stop-work-lifecycle', 'remote_control_stop.go', 'func (p *goalRPC) remoteWorkIdle', 'return p.reconcileNativeLifecycle()', 'return nil', 'TestRemoteControlFinalScopedLifecycle/stop/late-root'),
    ('retained-child-notice', 'remote_control_native_turn.go', 'func (p *goalRPC) nativeTurnNotification', ' && p.turnProofs[n.ThreadID].ThreadID != n.ThreadID', '', 'TestRemoteControlFinalScopedLifecycle/stop/late-child'),
    ('final-interleaving', 'remote_control_native_turn.go', 'func (p *goalRPC) reconcileScopedTurnProof', 'p.turnEvents[proof.Events:]', '[]remoteNativeTurn{}', 'TestRemoteControlFinalScopedLifecycle/stop/late-child-then-old-complete'),
    ('proof-refresh-reconciliation', 'remote_control_native_turn.go', 'func (p *goalRPC) recordNativeTurnProof', 'p.reconcileNativeProofRefresh(next)', 'error(nil)', 'TestRemoteControlProofRefreshCannotEraseConflict'),
    ('proof-refresh-pending-turn', 'remote_control_native_turn.go', 'func (p *goalRPC) reconcileNativeProofRefresh', 'len(pending) != 0', 'false', 'TestRemoteControlProofRefreshTransitions/initial/unresolved'),
    ('proof-refresh-old-cursor', 'remote_control_native_turn.go', 'func (p *goalRPC) reconcileNativeProofRefresh', 'old.ID != next.ID || old.Status != next.Status', 'true', 'TestRemoteControlProofRefreshCannotEraseConflict/completion/late-root-finished-then-old-complete'),
    ('last-event-thread-separation', 'remote_control_native_turn.go', 'func (p *goalRPC) scopedNativeTurnAgrees', 'p.turnEvents[i].ThreadID == thread', 'true', 'TestRemoteControlProofRefreshCannotEraseConflict/completion/matching-child'),
    ('interleaving-thread-separation', 'remote_control_native_turn.go', 'func (p *goalRPC) reconcileScopedTurnProof', 'event.ThreadID == thread', 'true', 'TestRemoteControlProofRefreshCannotEraseConflict/completion/matching-child'),
    ('omitted-proof-recording', 'remote_control_native_turn.go', 'func (p *goalRPC) recordNativeTurnProof', 'p.turnProofs[p.thread] = next', 'if false { p.turnProofs[p.thread] = next }', 'TestRemoteControlLateNativeTurnCannotCertifyCompletion/late-own'),
]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default='go')
    parser.add_argument('--only', action='append', default=[])
    args = parser.parse_args()
    selected = [m for m in MUTANTS if not args.only or m[0] in args.only]
    if args.only and set(args.only) != {m[0] for m in selected}:
        parser.error('unknown mutation name')
    env = dict(os.environ)
    env['TMPDIR'] = '/tmp'
    files = {name: ROOT / 'cmd/divybot' / name for _, name, *_ in MUTANTS}
    originals = {name: path.read_text() for name, path in files.items()}
    failed = False
    try:
        baseline = subprocess.run([args.go, 'test', './cmd/divybot', '-run', '^TestRemoteControl', '-count=1'], cwd=ROOT, env=env, capture_output=True, text=True, timeout=180)
        if baseline.returncode:
            print('baseline FAIL', flush=True)
            print(baseline.stdout + baseline.stderr)
            return 1
        print('baseline GREEN', flush=True)
        for name, file, function, old, new, control in selected:
            path = files[file]
            try:
                code = function_replace(originals[file], function, old, new) if function else replace(originals[file], old, new)
                path.write_text(code)
                result = subprocess.run([args.go, 'test', './cmd/divybot', '-run', '^' + control + '$', '-count=1'], cwd=ROOT, env=env, capture_output=True, text=True, timeout=90)
                output = result.stdout + result.stderr
                red = result.returncode != 0 and '--- FAIL:' in output and '[build failed]' not in output
                print(name + (' ASSERTION RED' if red else ' INVALID OR SURVIVED'), flush=True)
                if not red:
                    failed = True
                    print(output, flush=True)
            finally:
                path.write_text(originals[file])
    finally:
        for name, path in files.items():
            path.write_text(originals[name])
    return 1 if failed else 0

if __name__ == '__main__':
    sys.exit(main())
