package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syntheticAGYID = "00000000-0000-4000-8000-000000000001"

func TestAGYLateIdentityKeepsExactOccupantAndDurableAuthority(t *testing.T) {
	for _, change := range []string{"", "unconfirmed", "wrong-source", "wrong-pane", "wrong-name", "wrong-cwd", "not-ready", "changed-sequence", "changed-dispatch", "uncertain", "missing-id", "invalid-id", "changed-store"} {
		t.Run(change, func(t *testing.T) {
			root, j, receipt := claudeLiveBindingFixture(t)
			j.Agent, j.GoalDelivery = "agy", "confirmed"
			receipt.dispatch.Source = "agy"
			if receipt.writeDispatch("dispatched", receipt.dispatch.Location) != nil {
				t.Fatal("fixture dispatch")
			}
			cwd := filepath.Join(t.TempDir(), "checkout")
			directory, _ := agyStoreDirectory(cwd, receipt.dispatch.RunID)
			store := &nativeStore{Source: "agy", Directory: directory}
			if receipt.writeAGYStore(store) != nil {
				t.Fatal("fixture store")
			}
			if change == "unconfirmed" {
				j.GoalDelivery = "unknown"
			}
			reads := 0
			readAgent := func(_ context.Context, pane string) (AgentInfo, error) {
				reads++
				if pane != j.Pane {
					t.Fatal("read escaped exact pane")
				}
				a := AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: cwd, InteractiveReady: true, AgentStatus: "working", StateChangeSeq: 1}
				switch change {
				case "wrong-source":
					a.Agent = "claude"
				case "wrong-pane":
					a.PaneID = "other-pane"
				case "wrong-name":
					a.Name = "other-agent"
				case "wrong-cwd":
					a.Cwd = filepath.Join(cwd, "other")
				case "not-ready":
					a.InteractiveReady = false
				case "changed-sequence":
					a.StateChangeSeq = uint64(reads)
				}
				return a, nil
			}
			readID := func(_ context.Context, got *nativeStore) (string, nativeIdentityReason) {
				if got.Directory != directory || got.Source != "agy" {
					t.Fatal("lookup escaped retained store")
				}
				switch change {
				case "missing-id":
					return "", nativeUnavailable
				case "invalid-id":
					return "foreign/native/path", ""
				case "changed-dispatch":
					receipt.dispatch.Profile = "changed"
					_ = receipt.writeDispatch("dispatched", receipt.dispatch.Location)
				case "uncertain":
					_ = receipt.writeDispatch("uncertain", receipt.dispatch.Location)
				case "changed-store":
					path := filepath.Join(filepath.Dir(receipt.file), "binding.json")
					b, _ := os.ReadFile(path)
					var row map[string]any
					_ = json.Unmarshal(b, &row)
					changed := filepath.Join(t.TempDir(), ".divybot-native", j.DispatchKey, "agy")
					row["NativeStore"] = map[string]any{"source": "agy", "directory": changed}
					b, _ = json.Marshal(row)
					_ = os.WriteFile(path, b, 0600)
				}
				return syntheticAGYID, ""
			}
			bound := retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j, readAgent, readID)
			if bound != (change == "") {
				t.Fatal("late identity authority guard failed")
			}
			_, id, _ := loadNativeBindingReceipt(root, j.DispatchKey, j, "fixture/inbox", nil, "agy")
			if change == "" && id != syntheticAGYID || change != "" && id != "" {
				t.Fatal("unverified identity persisted")
			}
			if change == "" && retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j,
				func(context.Context, string) (AgentInfo, error) {
					t.Fatal("bound identity was polled again")
					return AgentInfo{}, nil
				}, readID) {
				t.Fatal("bound identity replaced")
			}
		})
	}
}

func TestAGYNativeMetadataIdentityRejectsForeignAndAmbiguousStores(t *testing.T) {
	for _, change := range []string{"", "missing", "two-roots", "foreign-trajectory", "foreign-cascade", "symlink-db", "symlink-conversations", "public-root"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".divybot-native", strings.Repeat("a", 64), "agy")
			if os.MkdirAll(filepath.Join(root, "conversations"), 0700) != nil {
				t.Fatal("fixture directory")
			}
			// Only synthetic metadata tables; Python is also the dispatch host's readonly adapter.
			script := `import pathlib,sqlite3,sys
r=pathlib.Path(sys.argv[1]);change=sys.argv[2];identity=sys.argv[3]
if change!='missing':
 s=sqlite3.connect(r/'conversation_summaries.db');s.execute('CREATE TABLE conversation_summaries(conversation_id TEXT,parent_conversation_id TEXT)');s.execute('INSERT INTO conversation_summaries VALUES(?,NULL)',(identity,))
 if change=='two-roots':s.execute('INSERT INTO conversation_summaries VALUES(?,NULL)',('00000000-0000-4000-8000-000000000002',))
 s.commit();s.close()
 d=sqlite3.connect(r/'conversations'/(identity+'.db'));d.execute('CREATE TABLE trajectory_meta(trajectory_id TEXT,cascade_id TEXT)');d.execute('INSERT INTO trajectory_meta VALUES(?,?)',('foreign' if change=='foreign-trajectory' else '10000000-0000-4000-8000-000000000001','foreign' if change=='foreign-cascade' else identity));d.commit();d.close()
 if change=='symlink-db':
  p=r/'conversations'/(identity+'.db');q=r/'moved.db';p.rename(q);p.symlink_to(q)
 if change=='symlink-conversations':
  p=r/'conversations';q=r/'moved';p.rename(q);p.symlink_to(q,target_is_directory=True)
 if change=='public-root':r.chmod(0o755)
`
			if _, err := matrixCommand(context.Background(), "", "python3", nil, "-c", script, root, change, syntheticAGYID); err != nil {
				t.Fatal("fixture sqlite")
			}
			id, reason := (Host{}).readAGYIdentity(context.Background(), &nativeStore{Source: "agy", Directory: root})
			if change == "" && (id != syntheticAGYID || reason != "") || change != "" && id != "" {
				t.Fatal("native metadata identity guard failed")
			}
		})
	}
}

func TestAGYStoreBindingRejectsWrongReservation(t *testing.T) {
	for _, change := range []string{"source", "key", "relative", "traversal", "missing"} {
		t.Run(change, func(t *testing.T) {
			r := registrationReceipt(t, "agy", Overrides{})
			if r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}) != nil {
				t.Fatal("fixture dispatch")
			}
			directory, _ := agyStoreDirectory(filepath.Join(t.TempDir(), "checkout"), r.dispatch.RunID)
			store := &nativeStore{Source: "agy", Directory: directory}
			switch change {
			case "source":
				store.Source = "claude"
			case "key":
				store.Directory = strings.Replace(directory, strings.Repeat("d", 64), strings.Repeat("b", 64), 1)
			case "relative":
				store.Directory = ".divybot-native/agy"
			case "traversal":
				store.Directory += "/../agy"
			case "missing":
				store = nil
			}
			if r.writeAGYStore(store) == nil {
				t.Fatal("foreign store acquired reservation authority")
			}
		})
	}
}

func TestAGYStoreSurvivesCheckoutCleanup(t *testing.T) {
	h, calls := registrationHost(t, "")
	cwd := filepath.Join(t.TempDir(), "checkout")
	writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
	r := registrationReceipt(t, "agy", Overrides{})
	r.dispatch.Source = "agy"
	if _, _, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "agy", Overrides{}, r); err != nil {
		t.Fatal("synthetic launch failed")
	}
	var directory string
	for _, call := range calls() {
		for i, arg := range call {
			if arg == "--app_data_dir" && i+1 < len(call) {
				directory = filepath.Clean(filepath.Join(h.Home, ".gemini", call[i+1]))
			}
		}
	}
	if directory == "" || directory == cwd || strings.HasPrefix(directory, cwd+string(os.PathSeparator)) {
		t.Fatal("native store is lost with checkout cleanup")
	}
	var binding struct {
		NativeStore     *struct{ Source, Directory string }
		NativeSessionID string
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "binding.json"))
	if err != nil || json.Unmarshal(raw, &binding) != nil || binding.NativeStore == nil ||
		binding.NativeStore.Source != "agy" || binding.NativeStore.Directory != directory || binding.NativeSessionID != "" {
		t.Fatal("private native store is unbound or an absent native session was invented")
	}
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal("synthetic cleanup failed")
	}
	if st, err := os.Stat(directory); err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		t.Fatal("private native store did not survive checkout cleanup")
	}
	public, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
	if err != nil || strings.Contains(string(public), directory) || strings.Contains(string(public), "NativeStore") {
		t.Fatal("native store escaped into public dispatch")
	}
}

func TestAGYUncertainClearsNativeStoreBinding(t *testing.T) {
	r := registrationReceipt(t, "agy", Overrides{})
	r.dispatch.Source = "agy"
	loc := &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}
	if r.writeDispatch("dispatched", loc) != nil {
		t.Fatal("synthetic dispatch failed")
	}
	p := filepath.Join(filepath.Dir(r.file), "binding.json")
	b, _ := os.ReadFile(p)
	var row map[string]any
	if json.Unmarshal(b, &row) != nil {
		t.Fatal("synthetic binding invalid")
	}
	if row == nil {
		row = map[string]any{}
	}
	row["NativeStore"] = map[string]any{"source": "agy", "directory": "/fixture/native"}
	row["NativeSessionID"] = privateTestID(t)
	b, _ = json.Marshal(row)
	if os.WriteFile(p, b, 0600) != nil || r.writeDispatch("uncertain", loc) != nil {
		t.Fatal("invalidation failed")
	}
	b, _ = os.ReadFile(p)
	if json.Unmarshal(b, &row) != nil {
		t.Fatal("invalidated binding unreadable")
	}
	row = nil
	_ = json.Unmarshal(b, &row)
	if _, ok := row["NativeStore"]; ok {
		t.Fatal("uncertain launch retained native store authority")
	}
	if _, ok := row["NativeSessionID"]; ok {
		t.Fatal("uncertain launch retained native identity")
	}
}
