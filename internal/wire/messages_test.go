package wire

import (
	"errors"
	"reflect"
	"testing"
)

// SPEC.md's wire table, transcribed. Each case is the struct a daemon writes and the exact
// bytes that go on the socket, both directions, so a field rename cannot pass unnoticed.
var (
	hostCases = []struct {
		name string
		msg  FromHost
		json string
	}{
		{
			"hello",
			&Hello{
				Name: "box", Fingerprint: "ab12", Running: []string{"s_a", "s_b"},
				MaxSandboxes: 4, Tags: []string{"arch:amd64", "os:linux", "driver:docker"},
			},
			`{"type":"hello","name":"box","fingerprint":"ab12","running":["s_a","s_b"],"max_sandboxes":4,"tags":["arch:amd64","os:linux","driver:docker"]}`,
		},
		{
			"hello with a join token",
			&Hello{
				Name: "box", Fingerprint: "ab12", Running: []string{},
				MaxSandboxes: 1, Tags: []string{}, JoinToken: "t0ken",
			},
			`{"type":"hello","name":"box","fingerprint":"ab12","running":[],"max_sandboxes":1,"tags":[],"join_token":"t0ken"}`,
		},
		{"heartbeat", &Heartbeat{Running: 2, Max: 4}, `{"type":"heartbeat","running":2,"max":4}`},
		{"sandbox.started", &SandboxStarted{SID: "s_x"}, `{"type":"sandbox.started","sid":"s_x"}`},
		{
			"sandbox.ended", &SandboxEnded{SID: "s_x", Reason: EndIdle},
			`{"type":"sandbox.ended","sid":"s_x","reason":"idle"}`,
		},
		{
			"sandbox.ended with a detail", &SandboxEnded{SID: "s_x", Reason: EndFailed, Detail: "no such image"},
			`{"type":"sandbox.ended","sid":"s_x","reason":"failed","detail":"no such image"}`,
		},
		{"pty.replay", &PtyReplay{Stream: 3}, `{"type":"pty.replay","stream":3}`},
		{"pty.closed", &PtyClosed{Stream: 3}, `{"type":"pty.closed","stream":3}`},
		{"port.open", &PortOpen{Stream: 5}, `{"type":"port.open","stream":5}`},
		{
			"port.error", &PortError{Stream: 5, Msg: "connection refused"},
			`{"type":"port.error","stream":5,"msg":"connection refused"}`,
		},
		{"port.close", &PortClose{Stream: 5}, `{"type":"port.close","stream":5}`},
	}

	cpCases = []struct {
		name string
		msg  FromCP
		json string
	}{
		{"hello.ok", &HelloOK{HostID: "h_x"}, `{"type":"hello.ok","host_id":"h_x"}`},
		{
			"hello.pending", &HelloPending{HostID: "h_x", Code: "K7QP-3M"},
			`{"type":"hello.pending","host_id":"h_x","code":"K7QP-3M"}`,
		},
		{"hello.rejected", &HelloRejected{Reason: "revoked"}, `{"type":"hello.rejected","reason":"revoked"}`},
		{
			"sandbox.create",
			&SandboxCreate{Spec: Spec{
				SID: "s_x", Image: "python:3.12", Cmd: []string{"bash", "-lc", "echo hi"},
				IdleTimeoutS: 1800, Env: map[string]string{"A": "1"}, SecretEnv: map[string]string{"K": "v"},
			}},
			`{"type":"sandbox.create","spec":{"sid":"s_x","image":"python:3.12","cmd":["bash","-lc","echo hi"],"idle_timeout_s":1800,"env":{"A":"1"},"secret_env":{"K":"v"}}}`,
		},
		{
			// Null cmd means the worker's own entry, and is not the same as an empty one.
			"sandbox.create with the default entry",
			&SandboxCreate{Spec: Spec{
				SID: "s_y", Image: "img", IdleTimeoutS: 60,
				Env: map[string]string{}, SecretEnv: map[string]string{},
			}},
			`{"type":"sandbox.create","spec":{"sid":"s_y","image":"img","cmd":null,"idle_timeout_s":60,"env":{},"secret_env":{}}}`,
		},
		{"sandbox.destroy", &SandboxDestroy{SID: "s_x"}, `{"type":"sandbox.destroy","sid":"s_x"}`},
		{
			"pty.open", &PtyOpen{SID: "s_x", Stream: 3, Size: Size{Cols: 120, Rows: 40}},
			`{"type":"pty.open","sid":"s_x","stream":3,"size":{"cols":120,"rows":40}}`,
		},
		{
			"pty.resize", &PtyResize{SID: "s_x", Size: Size{Cols: 80, Rows: 24}},
			`{"type":"pty.resize","sid":"s_x","size":{"cols":80,"rows":24}}`,
		},
		{"pty.close", &PtyClose{Stream: 3}, `{"type":"pty.close","stream":3}`},
		{
			"port.dial", &PortDial{SID: "s_x", Port: 8080, Stream: 7},
			`{"type":"port.dial","sid":"s_x","port":8080,"stream":7}`,
		},
		{"port.close", &PortClose{Stream: 7}, `{"type":"port.close","stream":7}`},
	}
)

func TestMarshalAndParse(t *testing.T) {
	for _, c := range hostCases {
		t.Run("host/"+c.name, func(t *testing.T) {
			roundTrip(t, c.msg, c.json, func(raw []byte) (Message, error) {
				m, err := ParseFromHost(raw)
				return m, err
			})
		})
	}
	for _, c := range cpCases {
		t.Run("cp/"+c.name, func(t *testing.T) {
			roundTrip(t, c.msg, c.json, func(raw []byte) (Message, error) {
				m, err := ParseFromCP(raw)
				return m, err
			})
		})
	}
}

func roundTrip(t *testing.T, msg Message, want string, parse func([]byte) (Message, error)) {
	t.Helper()
	got, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != want {
		t.Errorf("Marshal\n got %s\nwant %s", got, want)
	}

	back, err := parse([]byte(want))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(back, msg) {
		t.Errorf("parse\n got %#v\nwant %#v", back, msg)
	}
}

// A caller writes a literal without the type field; Marshal is what stamps it.
func TestMarshalStampsTheType(t *testing.T) {
	m := &Heartbeat{Running: 1, Max: 2}
	if m.Type != "" {
		t.Fatal("a literal should not carry a type")
	}
	if _, err := Marshal(m); err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeHeartbeat {
		t.Errorf("Type = %q after Marshal", m.Type)
	}
}

func TestEveryMessageIsCovered(t *testing.T) {
	seen := map[Type]struct{}{}
	for _, c := range hostCases {
		seen[marshalledType(t, c.msg)] = struct{}{}
	}
	for _, c := range cpCases {
		seen[marshalledType(t, c.msg)] = struct{}{}
	}
	for kind := range knownTypes {
		if _, ok := seen[kind]; !ok {
			t.Errorf("%q has no case in this file", kind)
		}
	}
}

func marshalledType(t *testing.T, m Message) Type {
	t.Helper()
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	kind, err := typeOf(b)
	if err != nil {
		t.Fatal(err)
	}
	return kind
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseFromHost([]byte(`not json`)); err == nil {
		t.Error("want an error")
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"no type", `{"sid":"s_x"}`, ErrNoType},
		{"unknown to both", `{"type":"sandbox.teleport"}`, ErrUnknownType},
		// The old TypeScript wire, which is the shape a frozen daemon would send.
		{"the pre-rename name", `{"type":"session.started","sid":"s_x"}`, ErrUnknownType},
		{"wrong direction", `{"type":"pty.open","sid":"s_x","stream":1}`, ErrDirection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseFromHost([]byte(tt.raw)); !errors.Is(err, tt.want) {
				t.Errorf("ParseFromHost(%s) error = %v, want %v", tt.raw, err, tt.want)
			}
		})
	}

	if _, err := ParseFromCP([]byte(`{"type":"heartbeat","running":1,"max":2}`)); !errors.Is(err, ErrDirection) {
		t.Error("a host message parsed as a cp one should be ErrDirection")
	}
}

// The two daemons deploy separately, so a field the peer does not know must be dropped
// rather than fail the frame.
func TestParseIgnoresUnknownFields(t *testing.T) {
	m, err := ParseFromHost([]byte(`{"type":"heartbeat","running":1,"max":2,"load":0.4}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, &Heartbeat{header: header{TypeHeartbeat}, Running: 1, Max: 2}) {
		t.Errorf("got %#v", m)
	}
}

// A worker that has nothing running may send either; both mean the same thing.
func TestNullAndEmptyRunningAgree(t *testing.T) {
	for _, raw := range []string{
		`{"type":"hello","name":"a","fingerprint":"f","running":null,"max_sandboxes":1,"tags":null}`,
		`{"type":"hello","name":"a","fingerprint":"f","running":[],"max_sandboxes":1,"tags":[]}`,
	} {
		m, err := ParseFromHost([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		hello := m.(*Hello)
		if len(hello.Running) != 0 || len(hello.Tags) != 0 {
			t.Errorf("%s: running %v tags %v", raw, hello.Running, hello.Tags)
		}
	}
}
