# Herdr role sessions

Set each host's `herdr_session` to the selected persistent server session. Use
separate host entries for operator, dispatch and diagnostics, with distinct host
names; each entry gets its own subscription and reconnect loop. A saved machine
must name the same account and session. Selection does not create a server.
Existing configurations continue to use the default session.

A launch records its selected server session, saved machine and terminal occupant
in private dispatch and job state. Native conversation identity remains a separate
binding. Waits use that selected session and Herdr's occupant-pinned API. Input,
completion, stop and cleanup retain the recorded scope after configuration changes.
Snapshots from a different scope cannot supply a job's identity. Restore never
adopts a different terminal into an existing binding; missing legacy scope stays
in the default session. Changed native identity needs independent verification.

The coordinator must provision role servers and matching observer selection
separately, then verify reconnect and restore. No role session is started by
configuration loading or by these changes.
