package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Managed Codex puts each lifecycle hook in its argv. Keep the native process
// pin usable for that real launch shape without exposing the arguments.
func TestRemoteTUIProcessFingerprintWithLifecycleSizedArgv(t *testing.T) {
	for _, large := range []bool{false, true} {
		name := "short"
		if large {
			name = "lifecycle-hooks"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := []string{"-c", "import sys; sys.stdin.buffer.read()"}
			if large {
				for i := 0; i < 8; i++ {
					args = append(args, strings.Repeat("h", 8192))
				}
			}
			child := exec.CommandContext(ctx, "python3", args...)
			in, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = in.Close(); _ = child.Process.Kill(); _ = child.Wait() }()
			var previous string
			for i := 0; i < 2; i++ {
				out, err := exec.CommandContext(ctx, "python3", "-c", remoteTUIProcessPython, strconv.Itoa(child.Process.Pid)).Output()
				var proof struct {
					Fingerprint string `json:"fingerprint"`
				}
				if err != nil || json.Unmarshal(out, &proof) != nil || !digestPattern.MatchString(proof.Fingerprint) {
					t.Fatal("the live owned process did not yield its private fingerprint")
				}
				if i > 0 && proof.Fingerprint != previous {
					t.Fatal("the same live process changed its fingerprint")
				}
				previous = proof.Fingerprint
			}
		})
	}
}
