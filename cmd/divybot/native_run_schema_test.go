package main

import (
	"bufio"
	"encoding/json"
	"math"
	"net"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// nativeRunSchema validates v against the JSON Schema 2020-12 subset used by
// docs/native-run-read.schema.json. Unknown keywords fail the test, so the
// validator cannot silently pass a schema it does not understand.
type nativeRunSchema struct {
	t    *testing.T
	defs map[string]any
}

func (s nativeRunSchema) ok(schema map[string]any, v any) bool {
	for key := range schema {
		switch key {
		case "$schema", "title", "$defs", "$ref", "type", "const", "enum", "pattern", "minLength", "maxLength", "format",
			"minimum", "maximum", "required", "additionalProperties", "properties", "items", "maxItems",
			"allOf", "oneOf", "not", "if", "then", "else":
		default:
			s.t.Fatalf("schema keyword %q is not supported by this validator", key)
		}
	}
	if ref, has := schema["$ref"].(string); has {
		def, known := s.defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !strings.HasPrefix(ref, "#/$defs/") || !known {
			s.t.Fatalf("unresolved $ref %q", ref)
		}
		if !s.ok(def, v) {
			return false
		}
	}
	if name, has := schema["type"].(string); has && !s.typed(name, v) {
		return false
	}
	if c, has := schema["const"]; has && !reflect.DeepEqual(c, v) {
		return false
	}
	if e, has := schema["enum"].([]any); has {
		found := false
		for _, x := range e {
			found = found || reflect.DeepEqual(x, v)
		}
		if !found {
			return false
		}
	}
	return s.scalar(schema, v) && s.object(schema, v) && s.array(schema, v) && s.combined(schema, v)
}

func (s nativeRunSchema) typed(name string, v any) bool {
	switch name {
	case "string":
		_, is := v.(string)
		return is
	case "object":
		_, is := v.(map[string]any)
		return is
	case "array":
		_, is := v.([]any)
		return is
	case "boolean":
		_, is := v.(bool)
		return is
	case "integer":
		f, is := v.(float64)
		return is && f == math.Trunc(f)
	}
	s.t.Fatalf("schema type %q is not supported by this validator", name)
	return false
}

func (s nativeRunSchema) scalar(schema map[string]any, v any) bool {
	if f, is := v.(float64); is {
		if n, has := schema["minimum"].(float64); has && f < n {
			return false
		}
		if n, has := schema["maximum"].(float64); has && f > n {
			return false
		}
	}
	str, is := v.(string)
	if !is {
		return true
	}
	if p, has := schema["pattern"].(string); has && !regexp.MustCompile(p).MatchString(str) {
		return false
	}
	if n, has := schema["minLength"].(float64); has && utf8.RuneCountInString(str) < int(n) {
		return false
	}
	if n, has := schema["maxLength"].(float64); has && utf8.RuneCountInString(str) > int(n) {
		return false
	}
	if f, has := schema["format"].(string); has {
		if f != "date-time" {
			s.t.Fatalf("format %q is not supported by this validator", f)
		}
		if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
			return false
		}
	}
	return true
}

func (s nativeRunSchema) object(schema map[string]any, v any) bool {
	obj, is := v.(map[string]any)
	if !is {
		return true
	}
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, k := range required {
		if _, present := obj[k.(string)]; !present {
			return false
		}
	}
	for k, value := range obj {
		sub, known := props[k].(map[string]any)
		if !known && schema["additionalProperties"] == false {
			return false
		}
		if known && !s.ok(sub, value) {
			return false
		}
	}
	return true
}

func (s nativeRunSchema) array(schema map[string]any, v any) bool {
	items, is := v.([]any)
	if !is {
		return true
	}
	if n, has := schema["maxItems"].(float64); has && len(items) > int(n) {
		return false
	}
	sub, has := schema["items"].(map[string]any)
	for _, item := range items {
		if has && !s.ok(sub, item) {
			return false
		}
	}
	return true
}

func (s nativeRunSchema) combined(schema map[string]any, v any) bool {
	all, _ := schema["allOf"].([]any)
	for _, sub := range all {
		if !s.ok(sub.(map[string]any), v) {
			return false
		}
	}
	if not, has := schema["not"].(map[string]any); has && s.ok(not, v) {
		return false
	}
	if cond, has := schema["if"].(map[string]any); has {
		branch := "else"
		if s.ok(cond, v) {
			branch = "then"
		}
		if sub, has := schema[branch].(map[string]any); has && !s.ok(sub, v) {
			return false
		}
	}
	if one, has := schema["oneOf"].([]any); has {
		matched := 0
		for _, sub := range one {
			if s.ok(sub.(map[string]any), v) {
				matched++
			}
		}
		return matched == 1
	}
	return true
}

func loadNativeRunSchema(t *testing.T) (nativeRunSchema, map[string]any) {
	t.Helper()
	var root map[string]any
	body, err := os.ReadFile("../../docs/native-run-read.schema.json")
	if err != nil || json.Unmarshal(body, &root) != nil {
		t.Fatal("native run schema unavailable")
	}
	defs, _ := root["$defs"].(map[string]any)
	return nativeRunSchema{t: t, defs: defs}, root
}

func nativeRunFrame(t *testing.T, line []byte) any {
	t.Helper()
	var v any
	if json.Unmarshal(line, &v) != nil {
		t.Fatal("frame is not JSON")
	}
	return v
}

// Every frame the real socket writes, and every request it accepts, matches
// exactly one branch of the published wire schema.
func TestNativeRunSchemaMatchesSocketOutput(t *testing.T) {
	c, _, socket := nativeRunSocketFixture(t)
	c.st.mu.Lock()
	completed := c.st.nativeRunRecordLocked(701, c.st.Jobs[701], c.cfg.Inbox)
	completed.Phase, completed.ObservedAt = "completed", stamp(time.Now())
	completed.CompletedAt = completed.ObservedAt
	c.st.Jobs[702] = &Job{Issue: 702, Host: "fixture-host", HerdrScope: &herdrScope{Session: "dispatch"},
		Label: "claude-702", Agent: "claude", DispatchKey: strings.Repeat("e", 64), NativeSessionID: "fixture-native-702"}
	c.st.CompletedRuns = map[int]completedRun{703: {NativeRun: &nativeRunRecord{}, Phase: "observed"}}
	c.st.mu.Unlock()
	schema, root := loadNativeRunSchema(t)
	requests := []string{
		`{"id":"watch","method":"run.subscribe","params":{}}`,
		`{"id":"lookup","method":"run.lookup","params":{"keys":[{"key":"701","label":"codex-701"},{"key":"702","label":"claude-702"},{"key":"703","label":"codex-703"},{"key":"704","label":"agy-704"}]}}`,
		`{"id":"refused","method":"agent.stop","params":{}}`,
	}
	conns := []net.Conn{}
	readers := []*bufio.Reader{}
	for _, raw := range requests {
		if schema.ok(root, nativeRunFrame(t, []byte(raw))) == strings.Contains(raw, "agent.stop") {
			t.Fatal("request schema disagrees with the server", raw)
		}
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		conn.Write([]byte(raw + "\n"))
		conns, readers = append(conns, conn), append(readers, bufio.NewReader(conn))
	}
	frames := [][]byte{}
	for _, r := range readers {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal("frame unanswered")
		}
		frames = append(frames, line)
	}
	c.st.mu.Lock()
	c.st.CompletedRuns[701] = completedRun{NativeRun: completed, DispatchKey: completed.DispatchKey, NativeSessionID: completed.NativeSessionID, Phase: "observed"}
	err := c.st.saveLocked()
	c.st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	event, err := readers[0].ReadBytes('\n')
	if err != nil {
		t.Fatal("change event missing")
	}
	frames = append(frames, event)
	again, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	again.SetDeadline(time.Now().Add(3 * time.Second))
	again.Write([]byte(requests[1] + "\n"))
	line, err := bufio.NewReader(again).ReadBytes('\n')
	if err != nil {
		t.Fatal("completed lookup unanswered")
	}
	frames = append(frames, line)
	var lookup struct {
		Result nativeRunLookup `json:"result"`
	}
	if json.Unmarshal(frames[1], &lookup) != nil || len(lookup.Result.Records) != 2 || len(lookup.Result.Missing) != 2 {
		t.Fatal("fixture did not produce active records and missing reasons", string(frames[1]))
	}
	if json.Unmarshal(line, &lookup) != nil || len(lookup.Result.Records) != 2 || lookup.Result.Records[0].Phase != "completed" {
		t.Fatal("fixture did not produce a completed record", string(line))
	}
	for i, frame := range frames {
		if !schema.ok(root, nativeRunFrame(t, frame)) {
			t.Fatalf("socket frame %d does not match the published schema: %s", i, frame)
		}
	}
	// The validator is not vacuous: drifted frames are rejected.
	for _, drift := range []func(map[string]any){
		func(m map[string]any) { m["result"].(map[string]any)["extra"] = true },
		func(m map[string]any) { nativeRunFirstRecord(m)["completedAt"] = nativeRunFirstRecord(m)["observedAt"] },
		func(m map[string]any) { delete(nativeRunFirstRecord(m), "scope") },
		func(m map[string]any) { nativeRunFirstRecord(m)["phase"] = "idle" },
		func(m map[string]any) { m["result"].(map[string]any)["revision"] = float64(1 << 54) },
	} {
		m := nativeRunFrame(t, frames[1]).(map[string]any)
		drift(m)
		if schema.ok(root, m) {
			t.Fatal("schema accepted a drifted frame")
		}
	}
}

func nativeRunFirstRecord(m map[string]any) map[string]any {
	return m["result"].(map[string]any)["records"].([]any)[0].(map[string]any)
}
