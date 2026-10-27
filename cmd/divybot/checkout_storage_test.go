package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckoutStoragePreservesExistingPaths(t *testing.T) {
	root := t.TempDir()
	legacy, disk := filepath.Join(root, "legacy"), filepath.Join(root, "disk")
	h := Host{WorkdirRoot: legacy, DiskWorkdirRoot: disk}
	ctx := context.Background()
	old := filepath.Join(legacy, "issue-7")
	next := filepath.Join(disk, "issue-7")
	check := func(want string) {
		t.Helper()
		got, err := h.checkoutDirectory(ctx, 7)
		if err != nil || got != want {
			t.Fatalf("checkout selection: %q %v", got, err)
		}
	}
	check(old) // missing mount keeps compatibility fallback
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(disk, 0700); err != nil {
		t.Fatal(err)
	}
	check(old) // adding the disk mount never moves in-flight or finished work
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	check(next)
	if err := os.MkdirAll(next, 0700); err != nil {
		t.Fatal(err)
	}
	check(next)
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkoutDirectory(ctx, 7); err == nil {
		t.Fatal("ambiguous existing checkouts accepted")
	}
	if got := h.jobCheckout(&Job{Issue: 7, Workdir: old}); got != old {
		t.Fatal("cleanup followed new config instead of recorded path")
	}
	h.DiskWorkdirRoot = "relative"
	if _, err := h.checkoutDirectory(ctx, 8); err == nil {
		t.Fatal("relative disk root accepted")
	}
}

// Exercise adoption through real cleanup so observed folders cannot redirect deletion.
func TestCodexTrustAdoptionCheckoutBoundaries(t *testing.T) {
	for _, name := range []string{"outside-roots", "nested-checkout", "disk-checkout", "legacy-checkout", "wrong-issue", "unclean-path", "invalid-roots", "unknown-host"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			h := Host{Name: "fixture", WorkdirRoot: filepath.Join(root, "legacy"), DiskWorkdirRoot: filepath.Join(root, "disk")}
			legacy := h.legacyCheckout(7)
			disk := filepath.Join(h.DiskWorkdirRoot, "issue-7")
			foreign := filepath.Join(root, "foreign", "issue-7")
			nested := filepath.Join(legacy, "nested", "issue-7")
			sibling := h.legacyCheckout(8)
			for _, dir := range []string{legacy, disk, foreign, nested, sibling} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cwd, want := foreign, ""
			refHost := h.Name
			switch name {
			case "nested-checkout":
				cwd = nested
			case "disk-checkout":
				cwd, want = disk, disk
			case "legacy-checkout":
				cwd, want = legacy, legacy
			case "wrong-issue":
				cwd = sibling
			case "unclean-path":
				cwd = legacy + "/../issue-7"
			case "invalid-roots":
				cwd, h.DiskWorkdirRoot = legacy, "relative"
			case "unknown-host":
				cwd, refHost = disk, "unconfigured"
			}
			c := newCoord(&Config{Hosts: []Host{h}, StateFile: filepath.Join(root, "state.json"), Targets: []Target{{Label: "work", Repo: "fixture/repo"}}})
			j, ok := c.adopt(7, Issue{Number: 7, Labels: []string{"work"}}, map[int]agentRef{7: {Host: refHost, Agent: "codex", Cwd: cwd}})
			if !ok {
				t.Fatal("live job was not adopted")
			}
			if j.Workdir != want {
				t.Error("adoption recorded a folder outside its matching managed checkout")
			}
			if err := c.st.save(); err != nil {
				t.Fatal(err)
			}
			c.st = loadState(c.st.path)
			j = c.st.Jobs[7]
			if j == nil {
				t.Fatal("adopted job did not persist")
			}
			if j.Workdir != want {
				t.Error("adopted checkout boundary did not persist")
			}
			if name == "unknown-host" {
				return // unknown hosts cannot authorize teardown
			}
			// Isolate fleet discovery and record the real rm target without touching a service.
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for command, script := range map[string]string{
				"herdr": "#!/bin/sh\nprintf '%s\\n' '{\"result\":{\"agents\":[]}}'\n",
				"rm":    "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CHECKOUT_REMOVAL\"\nexec /bin/rm \"$@\"\n",
			} {
				if err := os.WriteFile(filepath.Join(bin, command), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			removal := filepath.Join(root, "removed.txt")
			t.Setenv("CHECKOUT_REMOVAL", removal)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if !c.teardownConfirmed(context.Background(), 7, j, "teardown") {
				t.Fatal("adopted job teardown did not finish")
			}
			target := want
			if target == "" {
				target = legacy
			}
			if got, err := os.ReadFile(removal); err != nil || string(got) != "-rf\n"+target+"\n" {
				t.Fatal("teardown did not remove only the root issue checkout")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("root issue checkout remains after teardown")
			}
			for _, preserved := range []string{foreign, sibling} {
				if _, err := os.Stat(preserved); err != nil {
					t.Fatal("teardown removed an unrelated checkout")
				}
			}
			other := disk
			if target == disk {
				other = legacy
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatal("teardown removed the other storage root's checkout")
			}
		})
	}
}
