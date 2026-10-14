# Explicit native worktree creation

The bundled `orchid.worktrees` plugin exposes `create-worktree`. Install its
manifest with Herdr's documented plugin link command when the dispatcher binary
is available in the plugin account. It is separate from existing operator tools.

The owner must supply `checkout-policy.json` with mode 0600 in the plugin's
Herdr-managed state directory. Its two absolute, canonical directory fields are
`repoRoot` and `worktreesRoot`, naming a project's sibling `repo` and `worktrees`
directories. There is no implicit policy or destination.

Invoke the workspace action with selected JSON containing exactly `branch` and
`path`. The explicitly supplied absolute path must equal the authorized
worktrees directory joined with that branch. The native workspace context must
name the policy's repository checkout. Traversal, symlinks and an unregistered
existing destination are refused. Native discovery must confirm the repository;
a branch already checked out elsewhere is refused. An existing checkout at the
exact authorized path is opened rather than recreated.

The action calls documented Herdr worktree discovery, create and open primitives;
Herdr owns Git worktrees and grouping. Group membership grants no run authority.
The gate covers this plugin action. Herdr's independent built-in sidebar create
command has no documented policy interception in this version; operators must
use the policy action. Global enforcement of the sidebar needs a vendor gate.
