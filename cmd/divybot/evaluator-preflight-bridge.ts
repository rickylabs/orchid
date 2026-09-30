// Read-only process adapter. Model IDs and admission come from pinned Harness data (#270/#321).
// Catalog eligibility never supplies observed session/model evidence for dispatch.
const base = new URL(`file://${Deno.cwd()}/`);
try {
  const input = JSON.parse(await new Response(Deno.stdin.readable).text());
  const preflight = await import(new URL('packages/routing/matrix/opencode-preflight.ts', base).href);
  const authority = await import(new URL('packages/routing/matrix/delegation-matrix.ts', base).href);
  const errors = await import(new URL('packages/routing/matrix/launchability.ts', base).href);
  try {
    if (!['any', 'none'].includes(authority.MATRIX_AUTHORITY?.configuration?.roles[input.role]?.certifies)) throw new Error('routing-invalid');
    const result = await preflight.preflightWorkloadRoute({
      tier: input.tier, role: input.role, generatorModel: input.generatorModel,
      privilegedTierAuthorization: input.authorization,
      unavailableTransports: input.unavailableTransports, worktree: '.',
    });
    console.log(JSON.stringify({ status: 'launchable', launcher: result.launchability.launcher,
      model: result.model, logicalModel: result.logicalModel, family: result.family,
      effort: result.effort, catalogObservedAt: result.catalogObservedAt }));
  } catch (error) {
    console.log(JSON.stringify(error instanceof errors.RouteLaunchError
      ? { status: 'refused', reasonCode: error.code, model: error.model, launcher: error.launcher }
      : { status: 'refused', reasonCode: 'routing-invalid' }));
  }
} catch {
  console.log(JSON.stringify({ status: 'refused', reasonCode: 'source-contract-unavailable' }));
}
