package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the cgo-free driver, registered as "sqlite"

	"sandboxd/internal/wire"
)

// schemaVersion is written to PRAGMA user_version. Bump it when the schema changes:
// there are no migrations, and a database at another version is dropped.
const schemaVersion = 6

// SQLite is the concrete store. Consumers declare the narrow interface they need; this
// struct is the only thing that speaks SQL.
type SQLite struct {
	db *sql.DB
}

// Open opens or creates the database at path (":memory:" in tests), wiping it when it was
// written by another schema version.
func Open(path string, log *slog.Logger) (*SQLite, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("store: state directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection: SQLite with a single writer is the model here (DESIGN.md puts a
	// second CP instance out of scope), and ":memory:" would otherwise be a fresh empty
	// database per pooled connection.
	db.SetMaxOpenConns(1)

	s := &SQLite{db: db}
	if err := s.init(log, path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) init(log *slog.Logger, path string) error {
	if _, err := s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return fmt.Errorf("store: pragmas: %w", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("store: user_version: %w", err)
	}
	var tables int
	err := s.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sandboxes','sessions','hosts')`,
	).Scan(&tables)
	if err != nil {
		return fmt.Errorf("store: schema check: %w", err)
	}
	// Every row is disposable, so an old schema is dropped rather than refused: a unit
	// under Restart=always must not crash-loop on a version bump. `sessions` is the name
	// a TypeScript control plane left behind.
	if tables > 0 && version != schemaVersion {
		log.Warn("schema version changed; dropping all state",
			"path", path, "found", version, "expected", schemaVersion)
		_, err := s.db.Exec(
			`DROP TABLE IF EXISTS sandboxes; DROP TABLE IF EXISTS sessions; DROP TABLE IF EXISTS hosts;`,
		)
		if err != nil {
			return fmt.Errorf("store: drop: %w", err)
		}
	}
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("store: schema: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
		return fmt.Errorf("store: set user_version: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS hosts (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL, approve_code TEXT, max_sandboxes INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT '[]', last_seen_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sandboxes (
  id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, host_id TEXT REFERENCES hosts(id),
  status TEXT NOT NULL, ended_reason TEXT, ended_detail TEXT,
  image TEXT NOT NULL, cmd TEXT, env TEXT NOT NULL, tags TEXT NOT NULL DEFAULT '[]',
  idle_timeout_s INTEGER NOT NULL,
  created_at INTEGER NOT NULL, started_at INTEGER, ended_at INTEGER, unknown_since INTEGER
);
CREATE INDEX IF NOT EXISTS sandboxes_owner ON sandboxes(owner_id, created_at);
CREATE INDEX IF NOT EXISTS sandboxes_status ON sandboxes(status, created_at);
`

func now() int64 { return time.Now().UnixMilli() }

// --- hosts ---------------------------------------------------------------------------

const hostCols = `id, name, fingerprint, status, approve_code, max_sandboxes, tags, last_seen_at, created_at`

// Hosts lists every enrolled host, oldest first.
func (s *SQLite) Hosts() ([]Host, error) {
	return s.hosts(`SELECT ` + hostCols + ` FROM hosts ORDER BY created_at`)
}

// Host looks one up. The bool is false when there is no such row, which is not an error:
// a caller decides whether a missing host is a 404 or a new enrollment.
func (s *SQLite) Host(id string) (Host, bool, error) {
	return s.host(`SELECT `+hostCols+` FROM hosts WHERE id=?`, id)
}

func (s *SQLite) HostByFingerprint(fp string) (Host, bool, error) {
	return s.host(`SELECT `+hostCols+` FROM hosts WHERE fingerprint=?`, fp)
}

// InsertPendingHost writes a new host awaiting approval. CreatedAt and LastSeenAt are set
// here; Status is always pending.
func (s *SQLite) InsertPendingHost(h Host) error {
	tags, err := encodeStrings(h.Tags)
	if err != nil {
		return err
	}
	t := now()
	_, err = s.db.Exec(
		`INSERT INTO hosts (id,name,fingerprint,status,approve_code,max_sandboxes,tags,last_seen_at,created_at)
		 VALUES (?,?,?,'pending',?,?,?,?,?)`,
		h.ID, h.Name, h.Fingerprint, h.ApproveCode, h.MaxSandboxes, tags, t, t,
	)
	if err != nil {
		return fmt.Errorf("store: insert host: %w", err)
	}
	return nil
}

func (s *SQLite) ApproveHost(id string) error {
	return s.exec(`UPDATE hosts SET status='approved', approve_code=NULL WHERE id=?`, id)
}

func (s *SQLite) RevokeHost(id string) error {
	return s.exec(`UPDATE hosts SET status='revoked' WHERE id=?`, id)
}

// TouchHost records that the host was heard from, and refreshes whatever the patch names.
func (s *SQLite) TouchHost(id string, p HostPatch) error {
	var tags any
	if p.Tags != nil {
		encoded, err := encodeStrings(p.Tags)
		if err != nil {
			return err
		}
		tags = encoded
	}
	return s.exec(
		`UPDATE hosts SET last_seen_at=?, name=COALESCE(?,name),
		 max_sandboxes=COALESCE(?,max_sandboxes), tags=COALESCE(?,tags) WHERE id=?`,
		now(), nullable(p.Name), nullable(p.MaxSandboxes), tags, id,
	)
}

func (s *SQLite) hosts(query string, args ...any) ([]Host, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query hosts: %w", err)
	}
	return out, nil
}

func (s *SQLite) host(query string, args ...any) (Host, bool, error) {
	h, err := scanHost(s.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, false, nil
	}
	return h, err == nil, err
}

// scanner is what QueryRow and Rows have in common, so one scan function serves both.
type scanner interface{ Scan(dest ...any) error }

func scanHost(r scanner) (Host, error) {
	var (
		h    Host
		code sql.NullString
		tags string
	)
	err := r.Scan(&h.ID, &h.Name, &h.Fingerprint, &h.Status, &code,
		&h.MaxSandboxes, &tags, &h.LastSeenAt, &h.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Host{}, err
		}
		return Host{}, fmt.Errorf("store: scan host: %w", err)
	}
	h.ApproveCode = code.String
	if h.Tags, err = decodeStrings(tags); err != nil {
		return Host{}, err
	}
	return h, nil
}

// --- sandboxes -----------------------------------------------------------------------

const sandboxCols = `id, owner_id, host_id, status, ended_reason, ended_detail, image, cmd,
	env, tags, idle_timeout_s, created_at, started_at, ended_at, unknown_since`

// InsertSandbox writes a new sandbox as queued. Secrets are not a parameter: they never
// reach a column.
func (s *SQLite) InsertSandbox(sb Sandbox) error {
	cmd, err := encodeCmd(sb.Cmd)
	if err != nil {
		return err
	}
	env, err := encodeMap(sb.Env)
	if err != nil {
		return err
	}
	tags, err := encodeStrings(sb.Tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO sandboxes (id,owner_id,status,image,cmd,env,tags,idle_timeout_s,created_at)
		 VALUES (?,?,'queued',?,?,?,?,?,?)`,
		sb.ID, sb.OwnerID, sb.Image, cmd, env, tags, sb.IdleTimeoutS, sb.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: insert sandbox: %w", err)
	}
	return nil
}

func (s *SQLite) Sandbox(id string) (Sandbox, bool, error) {
	sb, err := scanSandbox(s.db.QueryRow(`SELECT `+sandboxCols+` FROM sandboxes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Sandbox{}, false, nil
	}
	return sb, err == nil, err
}

// Sandboxes lists newest first. An empty ownerID lists every owner's.
func (s *SQLite) Sandboxes(ownerID string) ([]Sandbox, error) {
	if ownerID == "" {
		return s.sandboxes(`SELECT ` + sandboxCols + ` FROM sandboxes ORDER BY created_at DESC`)
	}
	return s.sandboxes(
		`SELECT `+sandboxCols+` FROM sandboxes WHERE owner_id=? ORDER BY created_at DESC`, ownerID)
}

// Queued is the FIFO the scheduler drains, and the order queue positions are read from.
func (s *SQLite) Queued() ([]Sandbox, error) {
	return s.sandboxes(`SELECT ` + sandboxCols + ` FROM sandboxes WHERE status='queued' ORDER BY created_at`)
}

func (s *SQLite) ActiveOnHost(hostID string) ([]Sandbox, error) {
	return s.sandboxes(
		`SELECT `+sandboxCols+` FROM sandboxes WHERE host_id=? AND status IN ('creating','running')`,
		hostID)
}

func (s *SQLite) Active() ([]Sandbox, error) {
	return s.sandboxes(`SELECT ` + sandboxCols + ` FROM sandboxes WHERE status IN ('creating','running')`)
}

func (s *SQLite) MarkCreating(id, hostID string) error {
	return s.exec(`UPDATE sandboxes SET status='creating', host_id=? WHERE id=?`, hostID, id)
}

func (s *SQLite) MarkRunning(id string) error {
	return s.exec(
		`UPDATE sandboxes SET status='running', started_at=?, unknown_since=NULL WHERE id=?`,
		now(), id)
}

// MarkKnown clears the boot-time doubt: the host has re-reported this sandbox.
func (s *SQLite) MarkKnown(id string) error {
	return s.exec(`UPDATE sandboxes SET unknown_since=NULL WHERE id=?`, id)
}

// MarkEnded is idempotent by the `status<>'ended'` guard: the first reason wins, so a
// sweep racing a DELETE does not rewrite why the sandbox stopped.
func (s *SQLite) MarkEnded(id string, reason wire.EndReason, detail string) error {
	return s.exec(
		`UPDATE sandboxes SET status='ended', ended_reason=?, ended_detail=?, ended_at=?,
		 unknown_since=NULL WHERE id=? AND status<>'ended'`,
		string(reason), nullIfEmpty(detail), now(), id)
}

// MarkActiveUnknown runs once on CP boot: every active sandbox is doubted until its host
// reconnects and reports it.
func (s *SQLite) MarkActiveUnknown() error {
	return s.exec(
		`UPDATE sandboxes SET unknown_since=? WHERE status IN ('creating','running')`, now())
}

func (s *SQLite) sandboxes(query string, args ...any) ([]Sandbox, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query sandboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Sandbox
	for rows.Next() {
		sb, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query sandboxes: %w", err)
	}
	return out, nil
}

func scanSandbox(r scanner) (Sandbox, error) {
	var (
		sb                   Sandbox
		reason, detail, cmd  sql.NullString
		env, tags            string
		hostID               sql.NullString
		startedAt, endedAt   sql.NullInt64
		unknownSinceNullable sql.NullInt64
	)
	err := r.Scan(&sb.ID, &sb.OwnerID, &hostID, &sb.Status, &reason, &detail, &sb.Image,
		&cmd, &env, &tags, &sb.IdleTimeoutS, &sb.CreatedAt, &startedAt, &endedAt,
		&unknownSinceNullable)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Sandbox{}, err
		}
		return Sandbox{}, fmt.Errorf("store: scan sandbox: %w", err)
	}
	if hostID.Valid {
		sb.HostID = &hostID.String
	}
	sb.EndedReason = wire.EndReason(reason.String)
	sb.EndedDetail = detail.String
	sb.StartedAt = int64Ptr(startedAt)
	sb.EndedAt = int64Ptr(endedAt)
	sb.UnknownSince = int64Ptr(unknownSinceNullable)
	if cmd.Valid && cmd.String != "" {
		if sb.Cmd, err = decodeStrings(cmd.String); err != nil {
			return Sandbox{}, err
		}
	}
	if sb.Env, err = decodeMap(env); err != nil {
		return Sandbox{}, err
	}
	if sb.Tags, err = decodeStrings(tags); err != nil {
		return Sandbox{}, err
	}
	return sb, nil
}

// Dump renders a whole table as text. It exists for the one assertion that cannot be made
// any other way — that no secret has ever reached a column (SPEC.md, "Sandbox") — and it
// takes a fixed set of table names rather than a query, because the name is interpolated
// into the SQL and it must not grow into a query API.
func (s *SQLite) Dump(table string) (string, error) {
	if table != "sandboxes" && table != "hosts" {
		return "", fmt.Errorf("store: dump %q: not a table of ours", table)
	}
	rows, err := s.db.Query(`SELECT * FROM ` + table) //nolint:gosec // the name is checked above
	if err != nil {
		return "", fmt.Errorf("store: dump %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("store: dump %s: %w", table, err)
	}
	var out strings.Builder
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			return "", fmt.Errorf("store: dump %s: %w", table, err)
		}
		for i, c := range cells {
			fmt.Fprintf(&out, "%s=%s ", cols[i], c.(*sql.NullString).String)
		}
		out.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("store: dump %s: %w", table, err)
	}
	return out.String(), nil
}

// --- plumbing ------------------------------------------------------------------------

func (s *SQLite) exec(query string, args ...any) error {
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// encodeStrings always writes an array, never null: `tags` and the API's `[]` agree.
func encodeStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: encode list: %w", err)
	}
	return string(b), nil
}

// encodeCmd keeps nil distinct from empty: a null column means the image's default entry.
func encodeCmd(v []string) (any, error) {
	if v == nil {
		return nil, nil
	}
	s, err := encodeStrings(v)
	return s, err
}

func encodeMap(v map[string]string) (string, error) {
	if v == nil {
		v = map[string]string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: encode map: %w", err)
	}
	return string(b), nil
}

func decodeStrings(s string) ([]string, error) {
	out := []string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode list: %w", err)
	}
	return out, nil
}

func decodeMap(s string) (map[string]string, error) {
	out := map[string]string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode map: %w", err)
	}
	return out, nil
}

// nullable turns an unset patch field into SQL NULL, which COALESCE reads as "leave it".
func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}
