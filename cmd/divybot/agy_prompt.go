package main

import (
	"context"
)

// The existing isolated AGY store owns native identity. A new native user step
// proves input acceptance; Herdr events only wake the read, never prove it.
type agyPromptCursor struct {
	ID        string `json:"id"`
	UserIndex *int   `json:"userIndex,omitempty"`
}

const agyPromptQuery = agyNativeMetadataQuery + `
 db=connect(directory/(identity+'.db'))
 try:
  count,index=db.execute('SELECT count(*),max(idx) FROM steps WHERE step_type=14').fetchone()
  assert type(count)==int and 0<=count<=4096
  assert index is None or type(index)==int and 0<=index<4096
 finally:db.close()
 print(json.dumps({'id':identity,'userIndex':index}))
` + agyNativeQueryErrors

func (h Host) agyPromptCursor(ctx context.Context, store *nativeStore) (agyPromptCursor, error) {
	var cursor agyPromptCursor
	if store == nil || store.Source != "agy" || !agyTrustScope(store.Directory) {
		return cursor, errPromptUnconfirmed
	}
	out, err := h.agyTrustCommand(ctx, "exec python3 -c "+shq(agyPromptQuery)+" "+shq(store.Directory)+" 2>/dev/null")
	if err != nil || len(out) > 1024 || strictJSON([]byte(out), &cursor) != nil || (cursor.ID != "" && !agyConversationID.MatchString(cursor.ID)) {
		return cursor, errPromptUnconfirmed
	}
	return cursor, nil
}

func agyPromptObserved(before, after agyPromptCursor) bool {
	if !agyConversationID.MatchString(after.ID) || after.UserIndex == nil || *after.UserIndex < 0 || *after.UserIndex >= 4096 || (before.ID != "" && before.ID != after.ID) {
		return false
	}
	return before.UserIndex == nil || *after.UserIndex > *before.UserIndex
}

func (h Host) injectAGYGoal(ctx context.Context, expected AgentInfo, goal string, j *Job) error {
	if j == nil || j.Agent != "agy" || !digestPattern.MatchString(j.DispatchKey) || !agyIdentityOccupant(expected, j) || j.Host != h.Name {
		return errPromptUnconfirmed
	}
	directory, err := agyStoreDirectory(expected.Cwd, "orchid-"+j.DispatchKey)
	if err != nil {
		return errPromptUnconfirmed
	}
	store := &nativeStore{Source: "agy", Directory: directory}
	return h.withPromptEvents(ctx, expected, func(ctx context.Context, p *promptEvents) error {
		if _, err := h.waitAgent(ctx, expected); err != nil {
			return err
		}
		baseline, err := h.agyPromptCursor(ctx, store)
		if err != nil {
			return err
		}
		if _, err = p.observe(ctx); err != nil {
			return err
		}
		if err = h.send(ctx, expected.Name, goal); err != nil {
			return errPromptUnconfirmed
		}
		for {
			a, err := p.observe(ctx)
			if err != nil || !samePromptOccupant(expected, a) {
				return errPromptUnconfirmed
			}
			after, err := h.agyPromptCursor(ctx, store)
			if err == nil && agyPromptObserved(baseline, after) && ctx.Err() == nil {
				return nil
			}
			if !p.wait(ctx) {
				return errPromptUnconfirmed
			}
		}
	})
}
