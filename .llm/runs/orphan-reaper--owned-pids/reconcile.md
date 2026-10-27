# Reconcile

Fetched current Orchid main after implementation and mutation evidence: 6e384a6dbe87064068fc4f895174476e1fc95296, identical to the run baseline and merge base. No conflict or scope change is required.

Production code reviewed at c1c1e09 is unchanged by subsequent commits. The later source change only bounds the negative start-fence test after it receives an early start result. It is covered by the full race run and all 16 completed mutation/restoration controls.

All production constructors are reachable through existing dispatcher call paths. New production files are reached by the existing startup collector or shared command helper. All new test helpers are called by kernel/lifecycle tests.

Configured live-harness and cross-UID opt-in checks have explicit skips in the unprivileged full suite and are unproven locally. No production deployment evidence is claimed.
