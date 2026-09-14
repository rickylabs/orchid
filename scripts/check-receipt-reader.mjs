// Read-only guard controls against an explicitly supplied built Harness reader.
// Fixtures are synthetic; no live source root is read.
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, chmod, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, relative } from 'node:path';
import { pathToFileURL } from 'node:url';
const { readOrchidDispatches } = await import(pathToFileURL(process.env.READER_MODULE).href);
const selected = process.argv[2] ?? 'all';
for (const check of ['mode', 'relative', 'symlink', 'git']) {
  if (selected !== 'all' && selected !== check) continue;
  const scratch = await mkdtemp(join(tmpdir(), 'reader-control-'));
  const root = join(scratch, 'receipts'), key = 'a'.repeat(64);
  try {
    await mkdir(join(root, key, 'record'), { recursive: true, mode: 0o700 });
    await writeFile(join(root, key, 'record', 'dispatch.json'), JSON.stringify({
      schemaVersion: 1, runId: 'orchid-' + key, issue: { repo: 'example/inbox', number: 1 },
      parentRunId: null, source: 'codex', provider: 'fixture-router', model: 'fixture-model',
      effort: 'high', profile: 'leaf', state: 'dispatched',
      location: { paneId: 'fixture-pane', workspaceId: 'fixture-workspace' },
    }), { mode: 0o600 });
    assert.equal((await readOrchidDispatches(root)).dispatches.length, 1, 'private baseline must be readable');
    let input = root;
    if (check === 'mode') await chmod(root, 0o755);
    if (check === 'relative') input = relative(process.cwd(), root);
    if (check === 'symlink') { input = join(scratch, 'alias'); await symlink(root, input); }
    if (check === 'git') await mkdir(join(scratch, '.git'), { mode: 0o700 });
    const result = await readOrchidDispatches(input);
    assert.equal(result.degraded, true, `reader must reject ${check}`);
    assert.deepEqual(result.dispatches, [], `reader must withhold ${check}`);
    console.log(`PASS reader refuses ${check}`);
  } finally { await rm(scratch, { recursive: true, force: true }); }
}
