#!/usr/bin/env python3
"""Compile endpoint guard mutations, require assertion failures, restore bytes."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[2]
use_root = '--root' in sys.argv[1:]
mutants = [
    ('operator-directory', 'store', 'ownerNativePrivateDir(filepath.Dir(options.Socket), *options.OperatorUID)', 'ownerNativePrivateDir(filepath.Dir(options.Socket), os.Getuid())', True),
    ('operator-socket', 'port', '!ownerNativeOwned(bound, *s.options.OperatorUID)', '!ownerNativeOwned(bound, os.Getuid())', True),
    ('client-directory-owner', 'port', 'ownerNativePrivateDir(filepath.Dir(*socket), os.Getuid())', 'ownerNativePrivateDir(filepath.Dir(*socket), *serverUID)', True),
    ('client-socket-owner', 'port', '!ownerNativeOwned(info, os.Getuid())', '!ownerNativeOwned(info, *serverUID)', True),
    ('caller-peer', 'port', 'uid == *s.options.OperatorUID', 'uid >= 0', True),
    ('server-peer', 'port', 'uid != *serverUID', 'uid != uid', True),
    ('held-no-follow', 'linux', 'oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC', 'oPath|syscall.O_CLOEXEC', False),
    ('held-socket-type', 'linux', 'held.Mode()&os.ModeSocket == 0', 'false', False),
    ('held-inode', 'linux', '!os.SameFile(created, held)', '!os.SameFile(held, held)', False),
    ('held-daemon-owner', 'linux', '!ownerNativeOwned(held, os.Getuid())', 'false', True),
    ('held-chown', 'linux', 'syscall.Fchownat(fd, "", uid, -1, atEmptyPath) != nil', 'false', True),
    ('held-permissions', 'linux', 'os.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), 0600)', 'os.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), 0660)', False),
    ('setter-error', 'port', 'prepare(s.options.Socket, created, *s.options.OperatorUID) != nil', '(prepare(s.options.Socket, created, *s.options.OperatorUID) != nil && false)', False),
    ('registered-inode', 'port', '!os.SameFile(created, bound)', '!os.SameFile(bound, bound)', False),
    ('registered-permissions', 'port', 'bound.Mode().Perm() != 0600', 'bound.Mode().Perm() != bound.Mode().Perm()', False),
    ('registered-owner', 'port', '!ownerNativeOwned(bound, *s.options.OperatorUID)', 'false', True),
    ('client-private-permissions', 'port', 'info.Mode().Perm() != 0600', 'info.Mode().Perm() != info.Mode().Perm()', True),
]
paths = {key: root / 'cmd/divybot' / ('owner_native_grant_' + name + '.go')
         for key, name in [('store', 'store'), ('port', 'port'), ('linux', 'linux')]}
originals = {key: path.read_bytes() for key, path in paths.items()}
results = []
try:
    with tempfile.TemporaryDirectory(prefix='endpoint-mutants-') as temp:
        os.chmod(temp, 0o711)
        binary = Path(temp) / 'endpoint.test'
        for label, key, before, after, needs_root in mutants:
            if needs_root and not use_root:
                continue
            source = originals[key].decode()
            assert source.count(before) == 1, label
            paths[key].write_text(source.replace(before, after))
            compiled = subprocess.run(['go', 'test', '-c', '-o', str(binary), './cmd/divybot'], cwd=root, capture_output=True, text=True)
            assert compiled.returncode == 0, label + ' failed compilation: ' + compiled.stderr
            command = [str(binary), '-test.run=^TestOwnerNative(EndpointTwoUIDs|SocketOwnershipNoFollow|SocketRegistrationGuards)$', '-test.count=1']
            if use_root:
                command = ['sudo', 'env', 'PATH=' + os.environ['PATH'], 'TMPDIR=' + os.environ.get('TMPDIR', '/tmp'), 'OWNER_ENDPOINT_REQUIRE_ROOT=1', *command]
            tested = subprocess.run(command, cwd=root, capture_output=True, text=True, timeout=60)
            output = tested.stdout + tested.stderr
            red = tested.returncode != 0 and '--- FAIL: TestOwnerNative' in output and 'panic:' not in output
            result = {'mutation': label, 'compileExitCode': compiled.returncode, 'testExitCode': tested.returncode, 'compiledAssertionRed': red, 'actualRootOperatorUIDs': use_root}
            results.append(result)
            print(json.dumps(result), flush=True)
            if not red:
                print(output, flush=True)
            paths[key].write_bytes(originals[key])
            assert red, label + ' did not fail a behavioral assertion'
finally:
    for key, path in paths.items():
        path.write_bytes(originals[key])
    restored = all(paths[key].read_bytes() == content for key, content in originals.items())
    print(json.dumps({'exactSourceRestored': restored, 'sourceSHA256': {key: hashlib.sha256(value).hexdigest() for key, value in originals.items()}, 'results': results}), flush=True)
    assert restored
