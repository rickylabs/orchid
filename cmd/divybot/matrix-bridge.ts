// Process adapter only. Policy and model identities come from the pinned NetScript source.
// No provider connection, account discovery or evaluator observation happens here.
const nonblank = (v: unknown): v is string => typeof v === "string" &&
  v.trim() === v && v.length > 0 && !/[\p{Cc}\u2028\u2029]/u.test(v);
const requireValue = (ok: unknown) => { if (!ok) throw new Error("matrix-refused"); };

async function main() {
  const input = JSON.parse(await new Response(Deno.stdin.readable).text());
  const base = new URL(`file://${Deno.cwd()}/`);
  const authority = await import(new URL(".llm/tools/agentic/runtime/delegation-matrix.ts", base).href);
  const policy = await import(new URL(".llm/tools/agentic/runtime/routing-policy.ts", base).href);
  const contract = await import(new URL(".llm/tools/agentic/runtime/contract.ts", base).href);
  let role = input.role?.replaceAll("-", "_") || "";
  let tier = input.tier || "";
  let coordinator = false;
  if (input.profileText) {
    const rows = input.profileText.split("\n").filter((line: string) => /^\|\s*`routing`\s*\|/.test(line));
    requireValue(rows.length === 1);
    const tokens = Array.from(rows[0].matchAll(/`([^`]+)`/g), (m: any) => m[1].replaceAll("-", "_"));
    const roles = tokens.filter((x) => authority.DELEGATION_ROLES.includes(x));
    const scopes = tokens.filter((x) => authority.COORDINATOR_TIERS.includes(x));
    if (roles.length && !scopes.length) {
      if (!role) role = roles[0];
      requireValue(roles.includes(role));
    } else {
      requireValue(!roles.length && scopes.length === 1 && /coordinator matrix/.test(rows[0]));
      requireValue(!role || role === "coordinator");
      requireValue(!tier || tier === scopes[0]);
      role = "coordinator";
      tier = scopes[0];
      coordinator = true;
    }
  }
  // Explicitly denied until an actual observed model/session contract is ratified.
  requireValue(!role.endsWith("_evaluation"));
  requireValue(coordinator || (authority.WORKLOAD_TIERS.includes(tier) && authority.DELEGATION_ROLES.includes(role)));
  const auth = input.authorization;
  if (auth) requireValue(["owner", "milestone_coordinator"].includes(auth.authorizer) && nonblank(auth.rationale));
  if (!coordinator) authority.assertPrivilegedTierAuthorization(tier, auth);
  const unavailableTransports = authority.MODEL_TRANSPORTS.filter((x: string) => !input.availableTransports.includes(x));
  // worktree is only echoed by this resolver; host placement binds the actual directory later.
  const request = { tier, role, worktree: ".", unavailableTransports, privilegedTierAuthorization: auth };
  const resolve = coordinator ? policy.resolveCoordinatorRoute : policy.resolveWorkloadRoute;
  let selected: any;
  try { selected = resolve(request); } catch { /* A trusted override can supply an otherwise unavailable cell. */ }
  const pin = input.pin;
  const matches = (route: any) => route && (!pin?.model || [route.logicalModel, route.model].includes(pin.model)) &&
    (!pin?.effort || [route.requestedEffort, route.effort].includes(pin.effort));
  if (!selected || !matches(selected)) {
    const grant = input.ownerMatrixOverride;
    requireValue(!coordinator && grant && pin && nonblank(grant.route?.model) && nonblank(grant.route?.effort));
    requireValue(Object.hasOwn(authority.MODEL_CATALOG, grant.route.model));
    requireValue(grant.route.effort === "provider_default" || contract.EFFORTS.includes(grant.route.effort));
    authority.assertOwnerMatrixOverride(tier, role, grant);
    // Existing source contract requires an exact human-readable worklog entry.
    const entry = authority.ownerMatrixOverrideWorklogEntry(tier, role, grant);
    requireValue(input.worklogText?.split(/\r?\n/).includes(entry));
    selected = resolve({ ...request, ownerMatrixOverride: grant });
    requireValue(matches(selected));
  }
  // The upstream override path omits the role restriction; it never waives this profile gate.
  requireValue(coordinator || authority.isTransportAllowedForRole(role, selected.transport, selected.family));
  requireValue(input.availableTransports.includes(selected.transport));
  for (const key of ["model", "logicalModel", "effort", "requestedEffort", "transport", "family"])
    requireValue(nonblank(selected[key]));
  requireValue(contract.EFFORTS.includes(selected.effort));

  const args = ["run", "--no-config", "--no-lock", ".llm/tools/agentic/runtime/cli/delegation-matrix-table.ts"];
  if (!coordinator) args.push("--tier", tier, "--role", role);
  args.push("--json");
  const child = new Deno.Command("deno", { args, stdout: "piped", stderr: "null" }).spawn();
  const deadline = setTimeout(() => { try { child.kill(); } catch { /* already exited */ } }, 20_000);
  const reader = child.stdout.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.length;
    if (size > 1024 * 1024) { child.kill(); throw new Error("matrix-refused"); }
    chunks.push(value);
  }
  const status = await child.status;
  clearTimeout(deadline);
  requireValue(status.success && size > 0);
  const raw = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { raw.set(chunk, offset); offset += chunk.length; }
  const table = JSON.parse(new TextDecoder().decode(raw));
  requireValue(table.schemaVersion === 1);
  const routes = coordinator ? table.coordinators?.[tier] : table.tiers?.find((x: any) => x.tier === tier)?.routes;
  requireValue(Array.isArray(routes));
  if (!selected.ownerMatrixOverride) requireValue(routes.some((x: any) => x.model === selected.logicalModel && x.effort === selected.requestedEffort));
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", raw)), (x) => x.toString(16).padStart(2, "0")).join("");
  console.log(JSON.stringify({ model: selected.model, logicalModel: selected.logicalModel,
    effort: selected.effort, requestedEffort: selected.requestedEffort, transport: selected.transport,
    family: selected.family, tier, role, digest }));
}

try { await main(); } catch { Deno.exit(1); }
