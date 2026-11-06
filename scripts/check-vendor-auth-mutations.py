#!/usr/bin/env python3
"""Red/green guard controls for the native vendor startup and auth boundary.

Each compiled mutant reintroduces one way terminal text, an untyped value, a
stale generation or a repeated effect could decide AGY startup or a credential
action. Every mutant must fail an assertion; the unmutated tree must pass.
"""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
STARTUP = "cmd/divybot/agy_startup.go"
TRUST = "cmd/divybot/agy_trust.go"
AUTH = "cmd/divybot/vendor_auth.go"
EVENT = "cmd/divybot/vendor_lifecycle_event.go"
HOOK = "cmd/divybot/herdr_lifecycle.go"
MAIN = "cmd/divybot/main.go"
AGY_TESTS = "^(TestAGYStartupNeverReadsScreen|TestAGYOnboardingTypedFactRefusesBeforeSeat|TestAGYOnboardingReferencePreservesNativeState)$"
AUTH_TESTS = "^(TestVendorAuth|TestStartupAndAuthBoundaryNeverReadsScreen|TestFleetStatusCarriesVendorCursor)"
MUTATIONS = [
    ("typed onboarding false admitted", STARTUP, "if !*complete {", "if false {", AGY_TESTS),
    ("unproven onboarding admitted", TRUST, "if err := agyOnboardingComplete(cache); err != nil {", "if err := agyOnboardingComplete(cache); err != nil && false {", AGY_TESTS),
    ("failed onboarding read admitted", TRUST, "cache = nil // a missing or unreadable cache is unproven", 'cache = []byte(`{"onboardingComplete":true}`)', AGY_TESTS),
    ("foreign label admitted", STARTUP, "a.Cwd == cwd && a.Name == label", "a.Cwd == cwd", AGY_TESTS),
    ("display status gates registration", STARTUP, "\t\treturn unknown()\n\t}\n\treturn nil\n}", '\t\treturn unknown()\n\t}\n\tif a.AgentStatus != "idle" || !a.InteractiveReady {\n\t\treturn unknown()\n\t}\n\treturn nil\n}', AGY_TESTS),
    ("screen read reinstated at startup", STARTUP, "\ta, err := h.agentInfoOf(ctx, pane)", '\t_, _ = h.herdr(ctx, "pane", "read", pane, "--source", "visible")\n\ta, err := h.agentInfoOf(ctx, pane)', AGY_TESTS),
    ("untyped Claude failure accepted", AUTH, 'e.Event != "StopFailure" || e.Failure != claudeAuthFailure', 'e.Event != "StopFailure"', AUTH_TESTS),
    ("hook records free-text errors", HOOK, "event.get('error') in failures", "isinstance(event.get('error'),str)", AUTH_TESTS),
    ("receipt failure enum unchecked", EVENT, "!claudeStopFailures[e.Failure]", "false", AUTH_TESTS),
    ("stale label or checkout accepted", AUTH, "!jobOccupant(a, j) || ", "", AUTH_TESTS),
    ("stale terminal or session accepted", AUTH, "e == nil || !e.matches(a) ||", "e == nil ||", AUTH_TESTS),
    ("other Codex error accepted", AUTH, "code != codexAuthFailure", 'code == ""', AUTH_TESTS),
    ("non-failed Codex turn accepted", AUTH, 'status != "failed" || code', "code", AUTH_TESTS),
    ("effect fence removed", AUTH, "j.AuthSignal == o.Key", "false", AUTH_TESTS),
    ("effect runs without a durable fence", AUTH, "if err := c.st.saveLocked(); err != nil {", "if err := c.st.saveLocked(); err != nil && false {", AUTH_TESTS),
    ("reads not debounced to native changes", AUTH, "return !j.authObserved || ", "return true || ", AUTH_TESTS),
    ("untyped transports read", AUTH, "\t\treturn // no typed native auth source: unknown, no read", "\t\tobservation, err = claudeAuthRead(read, host, j)", AUTH_TESTS),
    ("terminal read reinstated in auth", AUTH, "\ta, err := host.agentInfoOf(ctx, j.Pane)", '\t_, _ = host.herdr(ctx, "agent", "read", j.Pane, "--lines", "4")\n\ta, err := host.agentInfoOf(ctx, j.Pane)', AUTH_TESTS),
    ("supervision auth observation removed", MAIN, "c.superviseVendorAuth(ctx, host, j, ref, matched, recovering)", "_ = ref", AUTH_TESTS),
    ("displayed status gates auth", AUTH, "if !matched || recovering || j == nil {", 'if !matched || recovering || j == nil || ref.Status == "working" {', AUTH_TESTS),
    ("fleet receipt cursor dropped", MAIN, "VendorSeq: agentVendorSeq(a)}", "VendorSeq: 0}", AUTH_TESTS),
    ("receipt sequence ignored by debounce", AUTH, "ref.CompletionSeq, ref.VendorSeq}", "ref.CompletionSeq, 0}", AUTH_TESTS),
    ("debounce backstop removed", AUTH, " || now.Sub(j.authObservedAt) >= authObservationBackstop", "", AUTH_TESTS),
    ("unbound receipt moves the cursor", AUTH, "if a.VendorEvent == nil || !a.VendorEvent.matches(a) {", "if a.VendorEvent == nil {", AUTH_TESTS),
    ("unmatched occupant read", AUTH, "if !matched || recovering ||", "if recovering ||", AUTH_TESTS),
    ("undurable fence consumes the change", AUTH, "\tif _, err := c.applyVendorAuth(ctx, host, j, observation); err != nil {\n\t\treturn // the fence is not durable: keep this change pending\n\t}", "\t_, _ = c.applyVendorAuth(ctx, host, j, observation)", AUTH_TESTS),
    ("unreadable receipt taken as absent", AUTH, "\t\tif !read {", "\t\tif false && !read {", AUTH_TESTS),
    ("reader hides unreadable receipts", EVENT, "except Exception:print('{\"unreadable\":true}')", "except Exception:print('{}')", AUTH_TESTS),
]


def gate(label, green, selector):
    command = ["go", "test", "./cmd/divybot", "-run", selector, "-count=1", "-json"]
    p = subprocess.run(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=600)
    failures, passed, assertions = [], 0, []
    for line in p.stdout.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get("Action") == "pass" and event.get("Test"):
            passed += 1
        if event.get("Action") == "fail" and event.get("Test"):
            failures.append(event["Test"])
        output = event.get("Output", "").strip()
        if "_test.go:" in output and len(assertions) < 2:
            assertions.append(output)
    print(f"{label}: exit {p.returncode}; failed tests {len(failures)}; passed {passed}", flush=True)
    for line in assertions:
        print("  " + line, flush=True)
    if green:
        if p.returncode != 0 or passed == 0:
            raise RuntimeError("green control failed or executed no tests: " + label)
    elif p.returncode != 1 or not failures:
        raise RuntimeError("mutation survived or did not reach assertions: " + label)


def main():
    originals = {name: (ROOT / name).read_text() for name in {m[1] for m in MUTATIONS}}
    try:
        for selector in (AGY_TESTS, AUTH_TESTS):
            gate("control " + selector, True, selector)
        for label, filename, before, after, selector in MUTATIONS:
            original = originals[filename]
            if original.count(before) != 1:
                raise RuntimeError("mutation anchor is ambiguous or missing: " + label)
            (ROOT / filename).write_text(original.replace(before, after, 1))
            try:
                gate("mutant: " + label, False, selector)
            finally:
                (ROOT / filename).write_text(original)
        for selector in (AGY_TESTS, AUTH_TESTS):
            gate("restored " + selector, True, selector)
        print(f"PASS: {len(MUTATIONS)} compiled mutants failed assertions; restored controls passed", flush=True)
    finally:
        for filename, content in originals.items():
            (ROOT / filename).write_text(content)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL/INCONCLUSIVE: " + str(error), file=sys.stderr)
        sys.exit(2)
