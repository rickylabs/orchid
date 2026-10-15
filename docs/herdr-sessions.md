# Herdr role sessions

Set each host's `herdr_session` to the selected persistent server session. Use
separate host entries with distinct names only for endpoints eligible to receive
dispatched work; each entry gets its own subscription and reconnect loop. A saved
machine must name the same account and session. Selection does not create a server.
Existing configurations continue to use the default session.

Session selection does not reserve a host for a role. Every configured host is a
placement candidate: ordinary placement uses capability, agent eligibility and
free capacity; owner placement uses capability and load, ignoring capacity and
agent restrictions. Keep endpoints intended only for observation or manual work
out of the dispatcher host list until explicit placement isolation is available.

A launch records its selected server session, saved machine and terminal occupant
in private dispatch and job state. Native conversation identity remains a separate
binding. Waits use that selected session and Herdr's occupant-pinned API. Input,
completion, stop and cleanup retain the recorded scope after configuration changes.
Snapshots from a different scope cannot supply a job's identity. Restore never
adopts a different terminal into an existing binding; missing legacy scope stays
in the default session and original account, without adopting a newly configured
saved machine. Changed native identity needs independent verification.

The coordinator must provision role servers and matching observer selection
separately, then verify reconnect and restore. No role session is started by
configuration loading or by these changes.
