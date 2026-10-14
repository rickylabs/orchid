# Cockpit contract required for the native issue action

Status: required interface, not a deployed endpoint. No authorized Cockpit target
or native-context resolver is currently available to this plugin. Keep issue
#733 open. The existing scoped workspace action remains available. Do not add a
URL handler or guess issue identity from labels, cwd, selected text or metadata.

Cockpit must serve an authenticated `POST /api/herdr/issue-target` through its
existing API boundary. Accept this strict versioned input:

```ts
type HerdrIssueTargetRequestV1 = {
  schemaVersion: 1;
  serverScopeId: string;
  workspaceId: string;
  paneId: string;
};
```

`serverScopeId` is an opaque reference to an owner-authorized integration mapping
of account, saved machine and persistent server session. It comes from private
plugin configuration, not the invocation text. Workspace and pane come only from
Herdr's typed invocation context and must agree with its injected identifiers.
The server derives the terminal occupant and native conversation by authoritative
reads, then joins the exact dispatched Orchid receipt. It must verify the recorded
server session, terminal, pane, workspace, native identity and repository. A
restored/replaced occupant, uncertain receipt, unavailable server, or nonunique
join must fail closed. Native context is a lookup hint, never authorization.

Apply Cockpit's existing principal/session checks, repository grant and issueScope
read authorization to the resolved issue. Map no binding, an ambiguous binding
and an inaccessible issue to the same non-enumerating 404 response; return 401 for
an absent or expired authentication session and 503 for unavailable authority.
Limit request and response bodies to 4 KiB, execution to five seconds, and set
Cache-Control: no-store. Never redirect this resolver or return credentials,
socket paths, machine targets, terminal identifiers or native session identifiers.

Return only this typed success value, from existing issue/agent projections:

```ts
type HerdrIssueTargetV1 = {
  schemaVersion: 1;
  kind: 'cockpit.issue';
  repositoryId: string;
  issueNumber: number;
  relativePath: string;
  agentId: string;
  assignmentId: string;
  expectedRevision: string;
};
```

Cockpit must also serve the authenticated web route
`GET /projects/issue?repositoryId=<id>&issueNumber=<number>`, loading the existing
issue view after fresh session and repository authorization. The resolver's
relativePath must be exactly that route encoded from its resolved identifiers.
The plugin validates the closed response schema, positive integer issue number,
opaque agent/assignment identities and exact same-origin relative route. It may
combine this path only with the owner-configured authorized HTTPS Cockpit origin;
reject absolute targets, userinfo, protocol-relative paths, fragments, traversal
and extra query parameters. An unconfigured origin is an unavailable action.
Use the platform's normal URL opener with argv, never a shell command or caller
URL. No generic URL handler is required.

The local native plugin can register a pane-scoped `open-issue` action only after
this interface is served. It uses the existing authenticated client credential
boundary; credentials never go into the manifest, argv or event JSON. Workspace
inspection remains Herdr-owned. Any subsequent stop/steer/send/retry/raise-budget
uses Cockpit's existing issue-action admission with idempotencyKey,
expectedRevision and normal payload validation; resolving or opening the link
never submits an agent action or asserts lifecycle state.

Cockpit acceptance checks: granted versus revoked repository access, expired
session, missing/ambiguous binding, identical pane names in different sessions,
replaced/restored terminal, stale agent revision, malformed or cross-origin target,
request/response bounds and absence of private identifiers in the response. A
read or link open must have no dispatch or control side effect.
