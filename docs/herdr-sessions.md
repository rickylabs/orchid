# Herdr role sessions

A host can configure `herdr_roles`, a map from `dispatch`, `operator` and
`diagnostics` to endpoint objects containing `session` and optional `machine`.
A populated map requires `dispatch`; each configured role must use a nonempty,
distinct session name. Unknown roles and invalid selectors are rejected during
configuration loading. Omitted or empty maps retain the existing host's
`herdr_session` and `herdr_machine` behavior, including the default session.

Keep one host entry per physical placement candidate. Both ordinary dispatch
and owner `/swarm` select the host with the same capability, load, capacity and
agent rules as before, then select its `dispatch` endpoint. Retry preflight uses
that endpoint too and still refuses changes to recorded dispatch pins. Operator
and diagnostics endpoints are selected explicitly for observation or native
manual work; they are never alternatives for dispatch placement. No extra host
entries or capacity pools are created for roles.

Each role endpoint is complete: an omitted `machine` means the host's existing
account, without inheriting the base saved-machine selector. A supplied machine
must be a native saved profile for the host's account and the selected session;
the CLI catalog remains authoritative and unavailable profiles never fall back.
Session selection does not create a server.

Observers retain the base endpoint, explicitly configured role endpoints and
any endpoint still recorded by a job, deduplicated by host, session and machine.
Each distinct endpoint gets an independent subscription and reconnect loop.
A launch records its selected server session, saved machine and terminal occupant
in private dispatch and job state. Native conversation identity remains a separate
binding. Waits use that selected session and Herdr's occupant-pinned API. Input,
completion, stop and cleanup retain the recorded scope after configuration changes.
Snapshots from a different scope cannot supply a job's identity. Restore never
adopts a different terminal into an existing binding; missing legacy scope stays
in the default session and original account, without adopting a newly configured
saved machine. Changed native identity needs independent verification.

The coordinator must provision role servers and deploy configuration separately,
then verify controlled dispatch, reconnect and restore. Configuration loading and
these routing changes do not start role sessions or migrate existing jobs.
