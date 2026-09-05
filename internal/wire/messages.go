// Package wire is what the control plane and the worker agree on: the JSON control
// messages carried as text frames on the tunnel, the binary framing under them, and the
// id formats. It is a leaf — it imports nothing of ours and nothing outside the standard
// library, and `depguard` keeps it that way.
//
// The message table in SPEC.md is the contract; this file is its transcription. A field
// added here must be optional, or both daemons ship together — they deploy separately.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Type is the `type` field every control message carries.
type Type string

// host → cp
const (
	TypeHello          Type = "hello"
	TypeHeartbeat      Type = "heartbeat"
	TypeSandboxStarted Type = "sandbox.started"
	TypeSandboxEnded   Type = "sandbox.ended"
	TypePtyReplay      Type = "pty.replay"
	TypePtyClosed      Type = "pty.closed"
	TypePortOpen       Type = "port.open"
	TypePortError      Type = "port.error"
)

// cp → host
const (
	TypeHelloOK        Type = "hello.ok"
	TypeHelloPending   Type = "hello.pending"
	TypeHelloRejected  Type = "hello.rejected"
	TypeSandboxCreate  Type = "sandbox.create"
	TypeSandboxDestroy Type = "sandbox.destroy"
	TypePtyOpen        Type = "pty.open"
	TypePtyResize      Type = "pty.resize"
	TypePtyClose       Type = "pty.close"
	TypePortDial       Type = "port.dial"
)

// TypePortClose travels in both directions: either end may hang up a proxied connection.
const TypePortClose Type = "port.close"

// EndReason is why a sandbox stopped. It reaches the API as `ended_reason`.
type EndReason string

const (
	EndClosed EndReason = "closed" // DELETE, or the CP reclaiming an orphan
	EndIdle   EndReason = "idle"   // no PTY byte either way within idle_timeout_s
	EndFailed EndReason = "failed" // never started
	EndLost   EndReason = "lost"   // the host went away
	EndExited EndReason = "exited" // the process in the PTY returned
)

var (
	ErrNoType      = errors.New("wire: message has no type")
	ErrUnknownType = errors.New("wire: unknown message type")
	ErrDirection   = errors.New("wire: message travels the other way")
)

// Message is one control frame. Every message stamps its own discriminator, so callers
// write a struct literal and never repeat the type string; that is why the interface is
// satisfied by pointers.
type Message interface{ setType() }

// FromHost and FromCP are the two directions, kept apart in the type system: a tunnel
// writer accepts one of them, so a worker cannot send a cp→host message by accident.
type (
	FromHost interface {
		Message
		fromHost()
	}
	FromCP interface {
		Message
		fromCP()
	}
)

// header is embedded, not repeated: encoding/json promotes an anonymous struct's fields,
// so `type` is emitted first on every message and parsed back into the same place.
type header struct {
	Type Type `json:"type"`
}

// Size is a terminal's dimensions, in cells.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Spec is what the CP asks a host to run. Deliberately generic: the CP knows nothing
// about repos, agents or models — presets in the client turn those into env.
type Spec struct {
	SID   string `json:"sid"`
	Image string `json:"image"`
	// Cmd is exec'd in the PTY. Null means the worker's own default entry.
	Cmd          []string          `json:"cmd"`
	IdleTimeoutS int               `json:"idle_timeout_s"`
	Env          map[string]string `json:"env"`
	// SecretEnv is the only place secrets travel. Memory-only on both sides; the worker
	// drops its copy the moment the container has it, and no row ever holds one.
	SecretEnv map[string]string `json:"secret_env"`
}

// --- host → cp ---------------------------------------------------------------------

// Hello opens a tunnel. The fingerprint is the host's identity; the join token, when the
// operator set one on both sides, decides who approves rather than who it is.
type Hello struct {
	header
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	Running     []string `json:"running"`
	// MaxSandboxes is still configured as SANDBOXD_WORKER_MAX_SESSIONS on the worker:
	// NixOS modules and terraform envs already set that name (DESIGN.md decision 5).
	MaxSandboxes int      `json:"max_sandboxes"`
	Tags         []string `json:"tags"`
	JoinToken    string   `json:"join_token,omitempty"`
}

// Heartbeat is every 10 s, and is also what drains the queue.
type Heartbeat struct {
	header
	Running int `json:"running"`
	Max     int `json:"max"`
}

type SandboxStarted struct {
	header
	SID string `json:"sid"`
}

type SandboxEnded struct {
	header
	SID    string    `json:"sid"`
	Reason EndReason `json:"reason"`
	Detail string    `json:"detail,omitempty"`
}

// PtyReplay marks the boundary: binary replay frames follow on this stream, then live
// bytes. The viewer needs it to know the tail is history, not new output.
type PtyReplay struct {
	header
	Stream uint32 `json:"stream"`
}

type PtyClosed struct {
	header
	Stream uint32 `json:"stream"`
}

type PortOpen struct {
	header
	Stream uint32 `json:"stream"`
}

type PortError struct {
	header
	Stream uint32 `json:"stream"`
	Msg    string `json:"msg"`
}

// --- cp → host ---------------------------------------------------------------------

type HelloOK struct {
	header
	HostID string `json:"host_id"`
}

type HelloPending struct {
	header
	HostID string `json:"host_id"`
	Code   string `json:"code"`
}

type HelloRejected struct {
	header
	Reason string `json:"reason"`
}

type SandboxCreate struct {
	header
	Spec Spec `json:"spec"`
}

type SandboxDestroy struct {
	header
	SID string `json:"sid"`
}

type PtyOpen struct {
	header
	SID    string `json:"sid"`
	Stream uint32 `json:"stream"`
	Size   Size   `json:"size"`
}

type PtyResize struct {
	header
	SID  string `json:"sid"`
	Size Size   `json:"size"`
}

type PtyClose struct {
	header
	Stream uint32 `json:"stream"`
}

type PortDial struct {
	header
	SID    string `json:"sid"`
	Port   int    `json:"port"`
	Stream uint32 `json:"stream"`
}

// --- both ---------------------------------------------------------------------------

// PortClose is symmetric: the CP sends it when the browser goes away, the worker when
// the socket inside the container does.
type PortClose struct {
	header
	Stream uint32 `json:"stream"`
}

func (m *Hello) setType()          { m.Type = TypeHello }
func (m *Heartbeat) setType()      { m.Type = TypeHeartbeat }
func (m *SandboxStarted) setType() { m.Type = TypeSandboxStarted }
func (m *SandboxEnded) setType()   { m.Type = TypeSandboxEnded }
func (m *PtyReplay) setType()      { m.Type = TypePtyReplay }
func (m *PtyClosed) setType()      { m.Type = TypePtyClosed }
func (m *PortOpen) setType()       { m.Type = TypePortOpen }
func (m *PortError) setType()      { m.Type = TypePortError }
func (m *HelloOK) setType()        { m.Type = TypeHelloOK }
func (m *HelloPending) setType()   { m.Type = TypeHelloPending }
func (m *HelloRejected) setType()  { m.Type = TypeHelloRejected }
func (m *SandboxCreate) setType()  { m.Type = TypeSandboxCreate }
func (m *SandboxDestroy) setType() { m.Type = TypeSandboxDestroy }
func (m *PtyOpen) setType()        { m.Type = TypePtyOpen }
func (m *PtyResize) setType()      { m.Type = TypePtyResize }
func (m *PtyClose) setType()       { m.Type = TypePtyClose }
func (m *PortDial) setType()       { m.Type = TypePortDial }
func (m *PortClose) setType()      { m.Type = TypePortClose }

func (*Hello) fromHost()          {}
func (*Heartbeat) fromHost()      {}
func (*SandboxStarted) fromHost() {}
func (*SandboxEnded) fromHost()   {}
func (*PtyReplay) fromHost()      {}
func (*PtyClosed) fromHost()      {}
func (*PortOpen) fromHost()       {}
func (*PortError) fromHost()      {}
func (*PortClose) fromHost()      {}

func (*HelloOK) fromCP()        {}
func (*HelloPending) fromCP()   {}
func (*HelloRejected) fromCP()  {}
func (*SandboxCreate) fromCP()  {}
func (*SandboxDestroy) fromCP() {}
func (*PtyOpen) fromCP()        {}
func (*PtyResize) fromCP()      {}
func (*PtyClose) fromCP()       {}
func (*PortDial) fromCP()       {}
func (*PortClose) fromCP()      {}

// Marshal encodes a message as the JSON text frame, stamping its type.
func Marshal(m Message) ([]byte, error) {
	m.setType()
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("wire: marshal: %w", err)
	}
	return b, nil
}

// ParseFromHost decodes a text frame the control plane received from a worker.
func ParseFromHost(raw []byte) (FromHost, error) {
	t, err := typeOf(raw)
	if err != nil {
		return nil, err
	}
	var m FromHost
	switch t {
	case TypeHello:
		m = &Hello{}
	case TypeHeartbeat:
		m = &Heartbeat{}
	case TypeSandboxStarted:
		m = &SandboxStarted{}
	case TypeSandboxEnded:
		m = &SandboxEnded{}
	case TypePtyReplay:
		m = &PtyReplay{}
	case TypePtyClosed:
		m = &PtyClosed{}
	case TypePortOpen:
		m = &PortOpen{}
	case TypePortError:
		m = &PortError{}
	case TypePortClose:
		m = &PortClose{}
	default:
		return nil, unknown(t)
	}
	return m, unmarshalInto(raw, t, m)
}

// ParseFromCP decodes a text frame a worker received from the control plane.
func ParseFromCP(raw []byte) (FromCP, error) {
	t, err := typeOf(raw)
	if err != nil {
		return nil, err
	}
	var m FromCP
	switch t {
	case TypeHelloOK:
		m = &HelloOK{}
	case TypeHelloPending:
		m = &HelloPending{}
	case TypeHelloRejected:
		m = &HelloRejected{}
	case TypeSandboxCreate:
		m = &SandboxCreate{}
	case TypeSandboxDestroy:
		m = &SandboxDestroy{}
	case TypePtyOpen:
		m = &PtyOpen{}
	case TypePtyResize:
		m = &PtyResize{}
	case TypePtyClose:
		m = &PtyClose{}
	case TypePortDial:
		m = &PortDial{}
	case TypePortClose:
		m = &PortClose{}
	default:
		return nil, unknown(t)
	}
	return m, unmarshalInto(raw, t, m)
}

func typeOf(raw []byte) (Type, error) {
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", fmt.Errorf("wire: %w", err)
	}
	if h.Type == "" {
		return "", ErrNoType
	}
	return h.Type, nil
}

// unknown separates "the other direction sent this" from "nobody knows this name": the
// first is a bug on the peer, the second is a peer from a future release.
func unknown(t Type) error {
	if _, ok := knownTypes[t]; ok {
		return fmt.Errorf("%w: %q", ErrDirection, t)
	}
	return fmt.Errorf("%w: %q", ErrUnknownType, t)
}

func unmarshalInto(raw []byte, t Type, m Message) error {
	// Unknown fields are ignored on purpose: the two daemons deploy separately, so an
	// older peer must survive a newer one's optional field.
	if err := json.Unmarshal(raw, m); err != nil {
		return fmt.Errorf("wire: %s: %w", t, err)
	}
	return nil
}

var knownTypes = map[Type]struct{}{
	TypeHello: {}, TypeHeartbeat: {}, TypeSandboxStarted: {}, TypeSandboxEnded: {},
	TypePtyReplay: {}, TypePtyClosed: {}, TypePortOpen: {}, TypePortError: {},
	TypeHelloOK: {}, TypeHelloPending: {}, TypeHelloRejected: {}, TypeSandboxCreate: {},
	TypeSandboxDestroy: {}, TypePtyOpen: {}, TypePtyResize: {}, TypePtyClose: {},
	TypePortDial: {}, TypePortClose: {},
}
