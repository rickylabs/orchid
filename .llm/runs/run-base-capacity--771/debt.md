# Accepted integration debt

D1 — The native goal-set API has no expected revision/status precondition. Private receipt, goal and latest turn are rechecked immediately after the durable attempt reservation, but an external pause in the final read/write interval can still be overwritten. The cost is an unintended resume of that same owned goal. Eliminating that final gap requires an upstream conditional goal mutation; inventing client-side locks cannot protect independent native clients. The bounded same-goal recovery requested by the operator proceeds with this documented native limitation.

Legacy temporary app-server connections are not accepted recovery debt: their automatic resume is refused visibly. Remote Control recovery uses the live TUI's shared canonical daemon and a bounded native-start proof.
