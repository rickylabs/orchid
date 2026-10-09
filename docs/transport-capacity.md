# Transport capacity and pacing

The private transport-availability snapshot says which matrix transports
autonomous admission would offer now. Each row has one closed reason code, so on
its own it cannot say whether a transport is out because every seat is taken,
because the operator configured no seats, because the native meter is unread,
or because the governor is pacing the account. `transportCapacity` answers that
question beside the rows, from the same tick's accounting.

## Availability reason order

A quota verdict names itself before the seat budget. An unread, stale or
expired meter, and a 5h or weekly ceiling, are reported with their own code even
when the governor has also reduced the budget to zero. `no-capacity` remains
for a nonpositive budget with a fresh meter under the ceiling. That is either a
full or disabled seat pool, or adaptive governor pacing; the capacity row says
which. The closed reason vocabulary is unchanged.

## Capacity rows

`transportCapacity` lists every matrix transport in the same order as
`transports`. Every row has exactly these eight fields. Nullable fields are
explicit `null`, never omitted.

| Field | Values |
| --- | --- |
| `transport` | `claude`, `codex`, `agy`, `opencode` |
| `capacity` | `free`, `full`, `disabled`, `unknown` |
| `capacityReason` | `null` when `free` or `full`; otherwise `seats-not-configured`, `seat-budget-missing` or `seats-config-invalid` |
| `maxActive` | Configured seats; `null` when unknown, and for OpenCode |
| `active` | Seats this dispatcher counts as occupied; `null` for OpenCode |
| `admissionCap` | Seats the governor lets admission use now; `null` when no seat budget was computed, and for OpenCode |
| `pacing` | `clear`, `limited`, `unknown`, `unmetered` |
| `pacingReason` | `null` when `clear` or `unmetered`; `governor-pacing`, `5h-ceiling` or `weekly-ceiling` when `limited`; `meter-unread`, `meter-stale`, `window-expired` or `ceiling-misconfigured` when `unknown` |

Capacity is physical. `full` means the occupied seats reach the configured seats.
The occupied count is the one admission uses. It counts a job whose status is
unknown or whose handles changed, and any run still completing. `disabled`
means the operator configured no seats: a governor `max_active` below one, no
AGY entry in `unmetered_transports`, or no OpenCode provider with seats.
`unknown` with `seat-budget-missing` means no target names the transport, so
admission computed no seat budget for it this tick. `seats-config-invalid`
means the OpenCode provider configuration cannot be read. Unknown never means
zero seats.

OpenCode seats are per provider. Its aggregate row is `free` when any pool in
`openCodeProviderPools` has a seat, and its counts are in those pools.

Pacing is the native subscription meter and the governor cap. It is never seat
evidence. `limited` with `governor-pacing` means the meter has headroom but the
governor holds the admission cap below the configured seats. AGY and OpenCode
have explicit seats and no subscription meter, so their pacing is `unmetered`.
A transport whose capacity is not `free` is never offered. Whether a limited
or unknown pacing state withheld a transport is the availability row's answer.

The snapshot describes autonomous admission. An authenticated exact owner
choice does not consult seats, pacing or the governor.

## Reader-first rollout

The current Harness governance reader accepts only the existing snapshot
fields, so the dispatcher omits `transportCapacity` unless
`matrix.transport_capacity` is `true`. Enable it only after the operator
installs a Harness reader that accepts the field. Collection adds no remote
call: the rows come from the configuration, the job ledger and the meter
reading admission already holds.
