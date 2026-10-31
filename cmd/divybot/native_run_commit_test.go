package main

import (
	"errors"
	"os"
	"testing"
)

func TestNativeRunDurabilityInvalidations(t *testing.T) {
	for _, stage := range []string{"file", "rename", "directory"} {
		t.Run(stage, func(t *testing.T) {
			c := nativeRunFixture(t)
			w := c.st.nativeRuns
			ch := make(chan struct{}, 1)
			w.subs[ch] = struct{}{}
			c.st.fs = &stateFS{}
			fail := errors.New("fixture durability loss")
			switch stage {
			case "file":
				c.st.fs.syncFile = func(*os.File) error { return fail }
			case "rename":
				c.st.fs.rename = func(string, string) error { return fail }
			case "directory":
				c.st.fs.syncDir = func(*os.File) error { return fail }
			}
			if c.st.save() == nil {
				t.Fatal("durability failure hidden")
			}
			if w.revision != 0 || !w.unproven {
				t.Fatal("failed durability certified a revision")
			}
			if len(ch) != 1 {
				t.Fatal("failed durability did not invalidate consumer")
			}
			<-ch
			if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
				t.Fatal("unproven state published")
			}
			c.st.fs = nil
			if err := c.st.save(); err != nil {
				t.Fatal(err)
			}
			if w.revision != 1 || w.unproven || len(ch) != 1 {
				t.Fatal("successful durability not published")
			}
			for n := 0; n < 100; n++ {
				if err := c.st.save(); err != nil {
					t.Fatal(err)
				}
			}
			if len(ch) != 1 || w.revision != 101 {
				t.Fatal("slow consumer grew event backlog or lost certified revision")
			}
		})
	}
}

func TestNativeRunRevisionBound(t *testing.T) {
	c := nativeRunFixture(t)
	w := c.st.nativeRuns
	w.revision = nativeRunSafeInteger
	previous := w.generation
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	if w.revision > nativeRunSafeInteger || w.generation == previous {
		t.Fatal("revision exceeded JS precision or silently wrapped")
	}
}
