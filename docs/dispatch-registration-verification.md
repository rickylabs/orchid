# Dispatch registration verification

MEASURED on the stacked source at 4168b15, based on the matrix branch at
007e8ba987830b2ca0447a4073ed10fae08da46a. All fixtures are synthetic.
Build artifact and temporary storage were outside the repository; temporary storage was
executable. The initial default-temp attempt was inconclusive because execution was denied.
No check result from that attempt is claimed as a pass.

## Mutation controls and restored source

MEASURED. Each mutation was applied alone, tested, and restored before the next command.
The first replaces the actual agent-start call with a successful empty envelope; the second
removes durable reservation persistence. Exit 1 is the expected negative verdict.

    omit-agent-registration
    command: go test ./cmd/divybot -run ^TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv$ -count=1
    exit: 1
    --- FAIL: TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv (1.71s)
        --- FAIL: TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv/codex (0.62s)
            registration_test.go:90: wanted workspace, environment, registration; got 2 calls
        --- FAIL: TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv/claude (0.79s)
            registration_test.go:90: wanted workspace, environment, registration; got 2 calls
        --- FAIL: TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv/opencode (0.16s)
            registration_test.go:90: wanted workspace, environment, registration; got 2 calls
        --- FAIL: TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv/agy (0.13s)
            registration_test.go:90: wanted workspace, environment, registration; got 2 calls
    FAIL
    FAIL	orch/cmd/divybot	1.710s
    FAIL
    
    skip-durable-reservation
    command: go test ./cmd/divybot -run ^TestRegistrationFenceSurvivesRestartAndRefusesRepeatedAttempts$ -count=1
    exit: 1
    --- FAIL: TestRegistrationFenceSurvivesRestartAndRefusesRepeatedAttempts (0.00s)
        registration_test.go:141: restart allowed a duplicate launch
    FAIL
    FAIL	orch/cmd/divybot	0.003s
    FAIL
    
    restored source
    command: go test -race ./... -count=1
    exit: 0
    ok  	orch/cmd/divybot	5.567s
    
    restored source
    command: go vet ./...
    exit: 0
    <no output>
    
    restored source
    command: go build -o $BUILD_ARTIFACT ./cmd/divybot
    exit: 0
    <no output>

## Live herdr negative control

MEASURED. Installed herdr 0.9.0. Command: `python3 scripts/check-registration-negative.py`.
Exit: 0. The script creates its own pane, runs a foreground command, checks the busy-pane
refusal, and closes only that pane. No model turn or issue dispatch was performed.

    busy-pane agent start exit: 1
    busy-pane agent start error: agent_pane_busy
    owned-pane cleanup exit: 0
    registration-negative: PASS

## Limits

PLANNED. Live issue 316 / leaf dispatch, goal receipt and working-state acceptance remain
coordinator-owned. These source checks and the negative control do not prove deployment.
Orchid merge authorization remains with the owner. Independent evaluator/supervisor
certification is not supplied by these checks.
