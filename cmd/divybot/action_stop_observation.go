package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A stop receipt proves delivery. These separate immutable observations prove
// only what was subsequently measured on the launch host.
type actionStopProcess struct {
	SchemaVersion int                     `json:"schemaVersion"`
	OperationID   string                  `json:"operationId"`
	RequestDigest string                  `json:"requestDigest"`
	GroupID       int                     `json:"groupId"`
	RootPID       int                     `json:"rootPid"`
	Members       []actionProcessIdentity `json:"members"`
}
type actionProcessIdentity struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}
type actionStopObservation struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationID   string `json:"operationId"`
	RequestDigest string `json:"requestDigest"`
	Kind          string `json:"kind"` // seat_absent | process_absent
	ObservedAt    string `json:"observedAt"`
}
type actionStopIndex struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationID   string `json:"operationId"`
	RequestDigest string `json:"requestDigest"`
}

// The dispatch record gives the feed a direct, bounded lookup for this stop.
// It never needs to enumerate the growing action receipt root on each watch.
func (c *Coord) actionPublishStopIndex(r actionReceipt) error {
	if r.Action != "stop" || r.Outcome != "accepted" || r.Reason != "workspace_close_delivered" {
		return nil
	}
	if !strings.HasPrefix(r.NativeRunID, "orchid-") {
		return errors.New("stop_index_run_invalid")
	}
	key := strings.TrimPrefix(r.NativeRunID, "orchid-")
	if !digestPattern.MatchString(key) || !actionIDPattern.MatchString(r.OperationID) || !digestPattern.MatchString(r.RequestDigest) {
		return errors.New("stop_index_binding_invalid")
	}
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, key, "record")
	if !privateReceiptRoot(record) {
		return errors.New("stop_index_record_unavailable")
	}
	index := actionStopIndex{SchemaVersion: 1, OperationID: r.OperationID, RequestDigest: r.RequestDigest}
	path := filepath.Join(record, "stop-action.json")
	if err := actionImmutableJSON(record, "stop-action.json", index); err != nil && !os.IsExist(err) {
		return err
	}
	var existing actionStopIndex
	if err := readPrivateActionJSON(path, &existing); err != nil || existing != index {
		return errors.New("stop_index_conflict")
	}
	return c.publishActionOwner(record, path)
}

const actionProcCapturePython = `import os,json,sys
root=int(sys.argv[1]); group=int(sys.argv[2])
def stat(pid):
 try:
  s=open('/proc/%d/stat'%pid).read(); f=s[s.rfind(')')+2:].split()
  return int(f[1]),int(f[2]),int(f[19]),f[0]
 except (OSError,ValueError,IndexError): return None
rows={int(n):stat(int(n)) for n in os.listdir('/proc') if n.isdigit()}
rows={p:v for p,v in rows.items() if v is not None and v[3] not in ('Z','X')}
if root not in rows or rows[root][1]!=group: sys.exit(2)
ids={root}; changed=True
while changed:
 changed=False
 for p,v in rows.items():
  if p not in ids and (v[1]==group or v[0] in ids): ids.add(p); changed=True
print(json.dumps({'members':[{'pid':p,'start':rows[p][2]} for p in sorted(ids)]}))`

const actionProcGonePython = `import os,json,sys
a=json.loads(sys.argv[1]); group=a['groupId']; old={x['pid']:x['start'] for x in a['members']}
def stat(pid):
 try:
  s=open('/proc/%d/stat'%pid).read(); f=s[s.rfind(')')+2:].split()
  return int(f[2]),int(f[19]),f[0]
 except (OSError,ValueError,IndexError): return None
alive=False
for n in os.listdir('/proc'):
 if not n.isdigit(): continue
 p=int(n); v=stat(p)
 if v is not None and v[2] not in ('Z','X') and (v[0]==group or old.get(p)==v[1]): alive=True; break
print(json.dumps({'gone':not alive}))`

func (c *Coord) actionStopProcess(ctx context.Context, h Host, pane, agent string) (*actionStopProcess, error) {
	if c.actions.stopProcess != nil {
		return c.actions.stopProcess(ctx, h, pane, agent)
	}
	out, err := h.herdr(ctx, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, err
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return nil, err
	}
	var info struct {
		ProcessInfo struct {
			GroupID   int `json:"foreground_process_group_id"`
			ShellPID  int `json:"shell_pid"`
			Processes []struct {
				PID  int    `json:"pid"`
				Name string `json:"name"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err = json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	root := 0
	for _, p := range info.ProcessInfo.Processes {
		if strings.EqualFold(p.Name, accountKey(agent)) {
			if root != 0 {
				return nil, errors.New("native_process_ambiguous")
			}
			root = p.PID
		}
	}
	group := info.ProcessInfo.GroupID
	if root <= 1 || group <= 1 || group == info.ProcessInfo.ShellPID {
		return nil, errors.New("native_process_unavailable")
	}
	script := "python3 -c " + shq(actionProcCapturePython) + " " + strconv.Itoa(root) + " " + strconv.Itoa(group)
	out, err = h.runRemote(ctx, script)
	if err != nil {
		return nil, errors.New("native_process_probe_unavailable")
	}
	var captured struct {
		Members []actionProcessIdentity `json:"members"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &captured) != nil || len(captured.Members) == 0 || len(captured.Members) > 512 {
		return nil, errors.New("native_process_probe_invalid")
	}
	found := false
	for _, p := range captured.Members {
		if p.PID == root && p.Start > 0 {
			found = true
		}
		if p.PID <= 1 || p.Start == 0 {
			return nil, errors.New("native_process_probe_invalid")
		}
	}
	if !found {
		return nil, errors.New("native_process_probe_invalid")
	}
	return &actionStopProcess{SchemaVersion: 1, GroupID: group, RootPID: root, Members: captured.Members}, nil
}
func (c *Coord) actionProcessGone(ctx context.Context, h Host, a actionStopProcess) (bool, error) {
	if c.actions.processGone != nil {
		return c.actions.processGone(ctx, h, a)
	}
	if a.SchemaVersion != 1 || a.GroupID <= 1 || a.RootPID <= 1 || len(a.Members) == 0 || len(a.Members) > 512 {
		return false, errors.New("process_anchor_invalid")
	}
	body, _ := json.Marshal(a)
	out, err := h.runRemote(ctx, "python3 -c "+shq(actionProcGonePython)+" "+shq(string(body)))
	if err != nil {
		return false, errors.New("native_process_probe_unavailable")
	}
	var result struct {
		Gone *bool `json:"gone"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &result) != nil || result.Gone == nil {
		return false, errors.New("native_process_probe_invalid")
	}
	return *result.Gone, nil
}
func (c *Coord) actionWorkspaceGone(ctx context.Context, h Host, id string) (bool, error) {
	if c.actions.workspaceGone != nil {
		return c.actions.workspaceGone(ctx, h, id)
	}
	out, err := h.herdr(ctx, "workspace", "get", id)
	if err == nil {
		return false, nil
	}
	var response herdrEnv
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if json.Unmarshal([]byte(line), &response) == nil && response.Error != nil {
			return response.Error.Code == "workspace_not_found", nil
		}
	}
	return false, errors.New("workspace_observation_unavailable")
}
func (c *Coord) actionObserveStops(ctx context.Context, root string) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	// Rotate the bounded probe window so a persistently unproven stop cannot
	// starve newer observations. Only accepted stops inside the ten-minute
	// observation window count toward the per-tick budget.
	start := 0
	if len(dirs) > 0 {
		start = int(time.Now().Unix()/30) % len(dirs)
	}
	inspected := 0
	for n := 0; n < len(dirs); n++ {
		entry := dirs[(start+n)%len(dirs)]
		if ctx.Err() != nil || inspected >= 2 {
			return
		}
		if !entry.IsDir() || !actionIDPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		var result actionReceipt
		if readPrivateActionJSON(filepath.Join(dir, "result.json"), &result) != nil || result.Action != "stop" || result.Outcome != "accepted" || result.Reason != "workspace_close_delivered" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, result.ObservedAt)
		if err != nil || time.Since(at) > 10*time.Minute || time.Since(at) < 0 {
			continue
		}
		var anchor actionStopProcess
		if readPrivateActionJSON(filepath.Join(dir, "stop-anchor.json"), &anchor) != nil || anchor.OperationID != result.OperationID || anchor.RequestDigest != result.RequestDigest {
			continue
		}
		host, ok := c.hosts[result.Host]
		if !ok {
			continue
		}
		seatFile := filepath.Join(dir, "seat-observed.json")
		procFile := filepath.Join(dir, "process-observed.json")
		_, seatErr := os.Lstat(seatFile)
		_, procErr := os.Lstat(procFile)
		if seatErr == nil && procErr == nil {
			continue
		}
		inspected++
		if os.IsNotExist(seatErr) {
			check, cancel := context.WithTimeout(ctx, 12*time.Second)
			agents, e := c.actionList(check, host)
			gone := false
			if e == nil {
				gone, e = c.actionWorkspaceGone(check, host, result.WorkspaceID)
				if gone {
					for _, a := range agents {
						if a.PaneID == result.PaneID || a.WorkspaceID == result.WorkspaceID {
							gone = false
							break
						}
					}
				}
			}
			cancel()
			if e == nil && gone {
				c.actionWriteStopObservation(dir, result, "seat_absent", seatFile)
			}
		}
		if os.IsNotExist(procErr) {
			check, cancel := context.WithTimeout(ctx, 12*time.Second)
			gone, e := c.actionProcessGone(check, host, anchor)
			cancel()
			if e == nil && gone {
				c.actionWriteStopObservation(dir, result, "process_absent", procFile)
			}
		}
	}
}
func (c *Coord) actionWriteStopObservation(dir string, r actionReceipt, kind, path string) {
	o := actionStopObservation{SchemaVersion: 1, OperationID: r.OperationID, RequestDigest: r.RequestDigest, Kind: kind, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := actionImmutableJSON(dir, filepath.Base(path), o); err != nil && !os.IsExist(err) {
		return
	}
	_ = c.publishActionOwner(dir, filepath.Join(dir, "intent.json"), filepath.Join(dir, "result.json"), filepath.Join(dir, "stop-anchor.json"), path)
}
