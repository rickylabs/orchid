package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func nativeFixtureFrame(w io.Writer, body []byte, opcode byte) error {
	head := []byte{0x80 | opcode}
	if len(body) < 126 {
		head = append(head, byte(len(body)))
	} else if len(body) < 65536 {
		head = append(head, 126, 0, 0)
		binary.BigEndian.PutUint16(head[len(head)-2:], uint16(len(body)))
	} else {
		head = append(head, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(head[len(head)-8:], uint64(len(body)))
	}
	_, err := w.Write(append(head, body...))
	return err
}
func readNativeFixtureFrame(r io.Reader) ([]byte, byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, 0, err
	}
	n := uint64(head[1] & 127)
	if n == 126 {
		b := make([]byte, 2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		n = uint64(binary.BigEndian.Uint16(b))
	} else if n == 127 {
		b := make([]byte, 8)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		n = binary.BigEndian.Uint64(b)
	}
	if n > 1048576 || head[1]&128 == 0 {
		return nil, 0, fmt.Errorf("fixture framing refused")
	}
	mask := make([]byte, 4)
	if _, err := io.ReadFull(r, mask); err != nil {
		return nil, 0, err
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, 0, err
	}
	for i := range data {
		data[i] ^= mask[i%4]
	}
	return data, head[0] & 15, nil
}

func canonicalFixtureHost(t *testing.T, mode string, handler func(string, map[string]any) any) Host {
	t.Helper()
	// Keep AF_UNIX paths beneath the kernel length bound; normal t.TempDir
	// names can be too long after the native relative suffix is appended.
	root, err := os.MkdirTemp("", "rc-")
	if err != nil {
		t.Fatal("socket fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(root, ".codex", "app-server-control")
	if os.MkdirAll(dir, 0700) != nil {
		t.Fatal("fixture directory unavailable")
	}
	path := filepath.Join(dir, "app-server-control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("fixture socket unavailable")
	}
	t.Cleanup(func() { _ = listener.Close() })
	if os.Chmod(path, 0600) != nil {
		t.Fatal("fixture socket mode unavailable")
	}
	if mode == "directory-mode" {
		_ = os.Chmod(dir, 0755)
	}
	if mode == "socket-mode" {
		_ = os.Chmod(path, 0660)
	}
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(connection)
		key := ""
		for lines := 0; lines < 64; lines++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Sec-WebSocket-Key:") {
				key = strings.TrimSpace(strings.TrimPrefix(line, "Sec-WebSocket-Key:"))
			}
			if line == "" {
				break
			}
		}
		digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(digest[:])
		if mode == "bad-accept" {
			accept = "wrong"
		}
		_, err = fmt.Fprintf(connection, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
		if err != nil {
			return
		}
		for frames := 0; frames < 40; frames++ {
			b, opcode, err := readNativeFixtureFrame(reader)
			if err != nil {
				return
			}
			if opcode == 10 {
				continue
			}
			var q struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(b, &q) != nil {
				return
			}
			if q.ID == 0 {
				continue
			}
			if mode == "deadline" {
				time.Sleep(200 * time.Millisecond)
				return
			}
			result := any(map[string]any{})
			if q.Method == "initialize" {
				result = map[string]string{"userAgent": "fixture"}
			} else if handler != nil {
				result = handler(q.Method, q.Params)
			}
			body, _ := json.Marshal(response(q.ID, result))
			if mode == "newline" {
				body = append(body, '\n')
			}
			if mode == "duplicate" {
				body = []byte(`{"id":1,"id":2,"result":{}}`)
			}
			if mode == "binary" {
				_ = nativeFixtureFrame(connection, body, 2)
				return
			}
			if mode == "masked" {
				_, _ = connection.Write([]byte{0x81, 0x80})
				return
			}
			if mode == "fragmented" {
				_, _ = connection.Write([]byte{0x01, 0})
				return
			}
			if mode == "oversized" {
				_, _ = connection.Write([]byte{0x81, 127, 0, 0, 0, 0, 0, 0x10, 0, 1})
				return
			}
			if mode == "ping" {
				if nativeFixtureFrame(connection, []byte("fixture"), 9) != nil {
					return
				}
			}
			if nativeFixtureFrame(connection, body, 1) != nil {
				return
			}
		}
	}()
	return Host{Home: root, Name: "fixture-host"}
}

func TestRemoteControlCanonicalSocketTransport(t *testing.T) {
	for _, mode := range []string{"valid", "ping", "directory-mode", "socket-mode", "bad-accept", "newline", "binary", "masked", "fragmented", "oversized", "duplicate", "deadline", "late"} {
		t.Run(mode, func(t *testing.T) {
			h := canonicalFixtureHost(t, mode, func(method string, _ map[string]any) any { return map[string]string{"status": "connected"} })
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			err := h.withCanonicalConnection(ctx, privateTestID(t), func(p *goalRPC) error {
				e := p.remoteConnected()
				if mode == "late" {
					cancel()
				}
				return e
			})
			if (err == nil) != (mode == "valid" || mode == "ping") {
				t.Fatal("canonical socket framing/ownership/deadline guard failed")
			}
			if err != nil && (strings.Contains(err.Error(), h.Home) || strings.Contains(err.Error(), "fixture")) {
				t.Fatal("native/private transport error leaked")
			}
		})
	}
}

// The positive transport controls use actual AF_UNIX/kernel credentials. Here
// substitute only the credential/stat input to cover foreign-owner refusals
// without requiring the test runner to gain another UID's privileges.
func TestRemoteControlForeignOwnerInputs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("SO_PEERCRED input control is Linux-specific")
	}
	for _, mode := range []string{"peer", "socket-owner", "directory-owner"} {
		t.Run(mode, func(t *testing.T) {
			h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { return map[string]string{"status": "connected"} })
			old := "struct.unpack('3i',s.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[1]"
			next := "(uid+1)"
			if mode != "peer" {
				old = "def run():"
				target := "app-server-control.sock"
				if mode == "directory-owner" {
					target = "app-server-control"
				}
				next = "original_lstat=os.lstat\ndef foreign_stat(path):\n result=original_lstat(path)\n if os.path.basename(path)==" + shq(target) + ":\n  fields=list(result);fields[4]=os.getuid()+1;return os.stat_result(fields)\n return result\nos.lstat=foreign_stat\ndef run():"
			}
			if strings.Count(canonicalCodexBridge, old) != 1 {
				t.Fatal("native credential input fixture drifted")
			}
			bridge := strings.Replace(canonicalCodexBridge, old, next, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := h.withGoalScript(ctx, "export HOME="+shq(h.Home)+"; exec python3 -c "+shq(bridge), privateTestID(t), func(p *goalRPC) error { return p.remoteConnected() })
			if err == nil {
				t.Fatal("foreign native owner input certified the canonical daemon")
			}
		})
	}
}

func TestRemoteControlCanonicalPreparationOverRealFraming(t *testing.T) {
	run := syntheticRemoteRun(t)
	expected := *run
	id := run.NativeSessionID
	proofs := make(chan bool, 2)
	h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
		switch method {
		case "remoteControl/status/read":
			return map[string]string{"status": "connected"}
		case "thread/start":
			config, _ := params["config"].(map[string]any)
			projects, _ := config["projects"].(map[string]any)
			proofs <- len(projects) == 1 && projects[expected.Cwd] != nil
			return remoteThreadFixture(&expected)
		case "thread/name/set":
			proofs <- params["threadId"] == id && params["name"] == expected.Name
			return map[string]any{}
		case "thread/read":
			return remoteThreadFixture(&expected)
		}
		return map[string]any{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if h.prepareRemoteCodex(ctx, run, nil) != nil || run.NativeSessionID != id {
		t.Fatal("native preparation failed canonical framing or scoped trust/name readback")
	}
	for i := 0; i < 2; i++ {
		select {
		case ok := <-proofs:
			if !ok {
				t.Fatal("native preparation violated scope or name")
			}
		default:
			t.Fatal("native preparation missed a required effect")
		}
	}
	foreign := privateTestID(t)
	if h.forJob(&Job{Agent: "codex", RemoteControl: run}).withGoalConnection(ctx, foreign, func(*goalRPC) error { t.Fatal("foreign thread gained goal transport"); return nil }) == nil {
		t.Fatal("retained run allowed wrong native identity")
	}
}
