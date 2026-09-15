//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// All processes in this test are disposable helpers. No namespace-wide reaper runs
// in the test runner: the kernel scenario is isolated in a child subreaper.
func TestReaperKernelAdoptionAndLiveChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := reaperHelper(ctx, "scenario")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Helper failures have fixed assertion messages; never echo its process ids or paths.
		for _, reason := range []string{"reaper blocked waiting for a live inherited child", "reaper changed the live inherited child", "reaper stole an owned child's exit status", "INCONCLUSIVE: kernel subreaper contract unavailable"} {
			if strings.Contains(string(out), reason) {
				t.Log(reason)
			}
		}
		t.Fatal("isolated kernel reaper assertions failed")
	}
	if string(out) != "REAPER-KERNEL-PASS\n" {
		t.Fatal("kernel scenario did not produce its completion receipt")
	}
}

func reaperHelper(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReaperHelper$", "-test.timeout=12s")
	cmd.Env = append(os.Environ(), "ORCHID_REAPER_TEST="+mode)
	return cmd
}

func TestReaperHelper(t *testing.T) {
	mode := os.Getenv("ORCHID_REAPER_TEST")
	if mode == "" {
		return
	}
	if mode == "live" {
		input := bufio.NewScanner(os.NewFile(3, "control"))
		output := os.NewFile(4, "report")
		for input.Scan() {
			switch input.Text() {
			case "parent":
				fmt.Fprintln(output, os.Getppid())
			case "ping":
				fmt.Fprintln(output, "alive")
			case "exit":
				os.Exit(23)
			}
		}
		os.Exit(23)
	}
	if mode == "parent" {
		cmd := reaperHelper(context.Background(), "live")
		cmd.ExtraFiles = []*os.File{os.NewFile(3, "control"), os.NewFile(4, "report")}
		if cmd.Start() != nil {
			os.Exit(2)
		}
		// Intentionally do not wait: this child must be adopted by the scenario process.
		os.Exit(0)
	}
	if mode != "scenario" {
		t.Fatal("unknown helper role")
	}
	// Linux offers the same authoritative orphan adoption/wait contract to a subreaper.
	// This is confined to this helper; production still enables reaping only at PID 1.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36 /* PR_SET_CHILD_SUBREAPER */, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatal("INCONCLUSIVE: kernel subreaper contract unavailable")
	}
	t.Cleanup(func() {
		// All fixture control writers are closed by defers before this cleanup runs.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var status syscall.WaitStatus
			_, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if err == syscall.ECHILD {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("fixture descendants did not terminate during cleanup")
	})
	orphanControl, orphanReport, parent := startReaperFixture(t, "parent")
	defer orphanControl.Close()
	defer orphanReport.Close()
	if parent.Wait() != nil {
		t.Fatal("fixture parent did not exit")
	}
	reports := bufio.NewScanner(orphanReport)
	fmt.Fprintln(orphanControl, "parent")
	if !reports.Scan() || reports.Text() != strconv.Itoa(os.Getpid()) {
		t.Fatal("kernel did not adopt the fixture orphan")
	}
	var gate childGate
	collected := make(chan int, 1)
	go func() { collected <- gate.reapOrphans() }()
	select {
	case n := <-collected:
		if n != 0 {
			t.Fatal("reaper claimed to collect a live inherited child")
		}
	case <-time.After(time.Second):
		orphanControl.Close() // cooperative release of this fixture, not a signal
		<-collected
		t.Fatal("reaper blocked waiting for a live inherited child")
	}
	fmt.Fprintln(orphanControl, "ping")
	if !reports.Scan() || reports.Text() != "alive" {
		t.Fatal("reaper changed the live inherited child")
	}
	fmt.Fprintln(orphanControl, "exit")
	deadline := time.Now().Add(3 * time.Second)
	count := 0
	for count == 0 && time.Now().Before(deadline) {
		count += gate.reapOrphans()
		if count == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if count != 1 || gate.reapOrphans() != 0 {
		t.Fatal("exited inherited child was not collected exactly once")
	}

	// A directly-owned child that already exited must remain waitable by os/exec.
	release := gate.hold()
	control, report, owned := startReaperFixture(t, "live")
	defer control.Close()
	defer report.Close()
	defer release()
	fmt.Fprintln(control, "exit")
	deadline = time.Now().Add(3 * time.Second)
	zombie := false
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", owned.Process.Pid))
		if err != nil {
			t.Fatal("owned fixture state unavailable")
		}
		tail := string(data)[strings.LastIndex(string(data), ")")+2:]
		if strings.HasPrefix(tail, "Z ") {
			zombie = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !zombie {
		t.Fatal("owned child did not become waitable")
	}
	n := gate.reapOrphans()
	err := owned.Wait()
	if n != 0 {
		t.Fatal("reaper stole an owned child's exit status")
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatal("os/exec did not retain its child's exit status")
	}
	fmt.Println("REAPER-KERNEL-PASS")
	os.Exit(0)
}

func startReaperFixture(t *testing.T, mode string) (*os.File, *os.File, *exec.Cmd) {
	t.Helper()
	input, control, err := os.Pipe()
	if err != nil {
		t.Fatal("control pipe unavailable")
	}
	report, output, err := os.Pipe()
	if err != nil {
		t.Fatal("report pipe unavailable")
	}
	cmd := reaperHelper(context.Background(), mode)
	cmd.ExtraFiles = []*os.File{input, output}
	if cmd.Start() != nil {
		t.Fatal("fixture process unavailable")
	}
	input.Close()
	output.Close()
	return control, report, cmd
}
