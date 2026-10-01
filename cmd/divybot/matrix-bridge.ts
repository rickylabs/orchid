// Process adapter only. Policy and model identities come from the pinned Harness source.
// No provider connection, account discovery or evaluator observation happens here.
const nonblank = (v: unknown): v is string => typeof v === "string" &&
  v.trim() === v && v.length > 0 && !/[\p{Cc}\u2028\u2029]/u.test(v);
const requireValue = (ok: unknown) => { if (!ok) throw new Error("matrix-refused"); };

let failure = "resolution-failed";
async function main() {
  const input = JSON.parse(await new Response(Deno.stdin.readable).text());
  const base = new URL(`file://${Deno.cwd()}/`);
  const authority = await import(new URL("packages/routing/matrix/delegation-matrix.ts", base).href);
  const policy = await import(new URL("packages/routing/matrix/routing-policy.ts", base).href);
  const contract = await import(new URL("packages/routing/matrix/contract.ts", base).href);
  let role = input.role?.replaceAll("-", "_") || "";
  let tier = input.tier || "";
  let coordinator = false;
  failure = "profile-invalid";
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
  if (authority.DELEGATION_ROLES.includes(role) && role.endsWith("_evaluation")) {
    console.log(JSON.stringify({ status: "inconclusive", reasonCode: "observer-unavailable" }));
    return;
  }
  failure = "routing-invalid";
  requireValue(coordinator || (authority.WORKLOAD_TIERS.includes(tier) && authority.DELEGATION_ROLES.includes(role)));
  failure = "authorization-invalid";
  const auth = input.authorization;
  if (auth) requireValue(["owner", "milestone_coordinator"].includes(auth.authorizer) && nonblank(auth.rationale));
  if (!auth) failure = "authorization-required";
  if (!coordinator) authority.assertPrivilegedTierAuthorization(tier, auth);
  failure = "resolution-failed";
  // Policy names billing/provider aliases; the dispatcher owns physical seats.
  // Derive aliases and exact CLI prefixes from the pinned catalog, not a model list.
  const providerOf = (model: unknown): string | null => {
    if (typeof model !== "string") return null;
    const match = /^([a-z0-9][a-z0-9._-]{0,63})\/[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$/.exec(model);
    return match?.[1] ?? null;
  };
  const providerList = input.openCodeProviders ?? [];
  requireValue(Array.isArray(providerList) && providerList.length <= 128 &&
    providerList.every((x: unknown) => typeof x === "string" && /^[a-z0-9][a-z0-9._-]{0,63}$/.test(x)) &&
    new Set(providerList).size === providerList.length);
  const providers = new Set(providerList);
  const capabilities = Object.values(authority.MODEL_CATALOG).flatMap((m: any) => m.capabilities ?? []);
  const aliases = new Set(capabilities.filter((c: any) => providerOf(c.model) &&
    (!c.launch || c.launch.harness === "opencode")).map((c: any) => c.transport));
  const unavailableTransports = authority.MODEL_TRANSPORTS.filter((x: string) => aliases.has(x)
    ? !input.availableTransports.includes("opencode") || !capabilities.some((c: any) =>
      c.transport === x && providers.has(providerOf(c.model)) && (!c.launch || c.launch.harness === "opencode"))
    : !input.availableTransports.includes(x));
  // worktree is only echoed by this resolver; host placement binds the actual directory later.
  const request = { tier, role, worktree: ".", unavailableTransports, privilegedTierAuthorization: auth };
  const resolve = coordinator ? policy.resolveCoordinatorRoute : policy.resolveWorkloadRoute;
  let selected: any;
  try { selected = resolve(request); } catch { /* A trusted override can supply an otherwise unavailable cell. */ }
  const pin = input.pin;
  const matches = (route: any) => route && (!pin?.model || [route.logicalModel, route.model].includes(pin.model)) &&
    (!pin?.effort || [route.requestedEffort, route.effort].includes(pin.effort));
  if (!selected || !matches(selected)) {
    failure = !selected && !pin ? "route-unavailable" : "override-required";
    const grant = input.ownerMatrixOverride;
    requireValue(!coordinator && grant && pin && nonblank(grant.route?.model) && nonblank(grant.route?.effort));
    failure = "override-invalid";
    requireValue(Object.hasOwn(authority.MODEL_CATALOG, grant.route.model));
    requireValue(grant.route.effort === "provider_default" || contract.EFFORTS.includes(grant.route.effort));
    requireValue(!input.pinName || grant.pin === input.pinName);
    authority.assertOwnerMatrixOverride(tier, role, grant);
    // Existing source contract requires an exact human-readable worklog entry.
    const entry = authority.ownerMatrixOverrideWorklogEntry(tier, role, grant);
    requireValue(input.worklogText?.split(/\r?\n/).includes(entry));
    selected = resolve({ ...request, ownerMatrixOverride: grant });
    requireValue(matches(selected));
  }
  failure = "resolution-failed";
  // The upstream override path omits the role restriction; it never waives this profile gate.
  requireValue(coordinator || authority.isTransportAllowedForRole(role, selected.transport, selected.family));
  let transport = selected.transport;
  let provider = selected.provider;
  let effort = selected.effort;
  if (aliases.has(transport)) {
    const physicalProvider = providerOf(selected.model);
    const capability = authority.MODEL_CATALOG[selected.logicalModel]?.capabilities?.find((c: any) =>
      c.transport === transport && c.model === selected.model);
    requireValue(selected.agent === "opencode" && capability && physicalProvider &&
      input.availableTransports.includes("opencode") && providers.has(physicalProvider) &&
      (!capability.launch || (capability.launch.harness === "opencode" && capability.launch.router === physicalProvider)));
    transport = "opencode";
    provider = physicalProvider;
    // A provider default is not the source resolver's historical concrete default variant.
    if (selected.requestedEffort === "provider_default") effort = "provider_default";
  } else {
    requireValue(selected.agent !== "opencode" && input.availableTransports.includes(transport));
  }
  for (const key of ["provider", "model", "logicalModel", "effort", "requestedEffort", "transport", "family"])
    requireValue(nonblank(selected[key]));
  requireValue(contract.EFFORTS.includes(selected.effort));
  requireValue(contract.PROVIDER_KINDS.includes(selected.provider));

  const args = ["run", "--no-config", "--no-lock", "packages/routing/matrix/cli/delegation-matrix-table.ts"];
  if (!coordinator) args.push("--tier", tier, "--role", role);
  args.push("--json");
  const child = new Deno.Command("deno", { args, stdout: "piped", stderr: "null" }).spawn();
  // Must stay below divybot's matrixResolveTimeout, so a slow nested CLI is refused here
  // with a JSON reason instead of the whole bridge being killed from outside.
  const deadline = setTimeout(() => { try { child.kill(); } catch { /* already exited */ } }, 60_000);
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
  console.log(JSON.stringify({ provider, model: selected.model, logicalModel: selected.logicalModel,
    effort, requestedEffort: selected.requestedEffort, transport,
    family: selected.family, tier, role, digest }));
}

try { await main(); } catch { console.log(JSON.stringify({ status: "refused", reasonCode: failure })); }
