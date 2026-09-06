// Command dev runs the whole stack in one terminal: the control plane, a worker on this
// machine, and the example UI, each line prefixed with who said it. Ctrl-C stops all
// three; a second Ctrl-C gives up on the ones that will not go.
//
// It is a development tool, not part of the product: nix/packages.nix builds only cmd/.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startupGrace is generous because `go run` compiles before it listens, and a cold build
// of the whole module is the slowest thing that happens here.
const startupGrace = 60 * time.Second

const (
	red     = 31
	yellow  = 33
	magenta = 35
	cyan    = 36
)

type server struct {
	name   string
	dir    string
	args   []string
	colour int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, paint(red, "\n"+err.Error()))
		os.Exit(1)
	}
}

func run() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	apiPort := envInt("SANDBOXD_PORT", 8080)
	uiPort := envInt("SANDBOXD_UI_PORT", 8081)

	env := os.Environ()
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("SANDBOXD_PORT", strconv.Itoa(apiPort))
	set("SANDBOXD_UI_PORT", strconv.Itoa(uiPort))
	setDefault(&env, "SANDBOXD_DB", filepath.Join(root, ".data", "cp.db"))
	setDefault(&env, "SANDBOXD_API_URL", fmt.Sprintf("http://localhost:%d", apiPort))
	setDefault(&env, "SANDBOXD_URL", fmt.Sprintf("ws://localhost:%d", apiPort))
	setDefault(&env, "SANDBOXD_WORKER_IDENTITY", filepath.Join(root, ".data", "host.json"))
	setDefault(&env, "SANDBOXD_LLM_BASE_URL", "https://llm.developing.company")
	// The control plane refuses to boot without a service token. Both sides get the same
	// one here, so local dev works and production still has to say what its token is.
	setDefault(&env, "SANDBOXD_SERVICE_TOKEN", "dev-token")

	// Nothing hot-reloads the daemons: a reload would orphan every PTY and container the
	// worker owns.
	servers := []server{
		{"api", root, []string{"go", "run", "./cmd/sandboxd-api"}, cyan},
		{"ui", filepath.Join(root, "examples", "ui"), []string{"bun", "run", "--hot", "src/main.ts"}, yellow},
		{"worker", root, []string{"go", "run", "./cmd/sandboxd-worker"}, magenta},
	}

	fmt.Println(boxed([]string{
		"STACK RUNNING",
		"",
		fmt.Sprintf("ui      http://localhost:%d", uiPort),
		fmt.Sprintf("api     http://localhost:%d   docs at /doc", apiPort),
		"worker  this machine; approve it in the UI with the printed code",
	}))

	group := &group{env: env, width: width(servers), exited: make(chan struct{})}
	defer group.stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go forceOnSecondSignal(ctx, group)

	// The API first: the UI forwards to it and the worker dials it, and a page already
	// open on the UI polls at once. Both cope with a restart, but the first start should
	// be quiet.
	listening := make(chan struct{})
	if err := group.start(servers[0], listening); err != nil {
		return err
	}
	select {
	case <-listening:
	case <-group.exited:
		return errors.New("api exited before listening — see its output above")
	case <-ctx.Done():
		return nil
	case <-time.After(startupGrace):
		return fmt.Errorf("api did not start within %s — see its output above", startupGrace)
	}

	for _, s := range servers[1:] {
		if err := group.start(s, nil); err != nil {
			return err
		}
	}
	select {
	case <-group.exited:
	case <-ctx.Done():
		fmt.Println("\nStopping... press Ctrl-C again to force.")
	}
	return nil
}

// group owns every child. Each runs in its own process group, so stopping one takes the
// binary `go run` built with it rather than leaving an orphan holding the port.
type group struct {
	env      []string
	width    int
	mu       sync.Mutex
	children []*child
	once     sync.Once
	exited   chan struct{}
}

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (g *group) start(s server, listening chan<- struct{}) error {
	cmd := exec.Command(s.args[0], s.args[1:]...) //nolint:gosec // the commands are the literals above
	cmd.Dir = s.dir
	cmd.Env = g.env
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}

	c := &child{cmd: cmd, done: make(chan struct{})}
	g.mu.Lock()
	g.children = append(g.children, c)
	g.mu.Unlock()

	tag := paint(s.colour, pad(s.name, g.width)) + " │ "
	var ready sync.Once
	announce := func() {
		if listening != nil {
			ready.Do(func() { close(listening) })
		}
	}
	var relays sync.WaitGroup
	relays.Add(2)
	for _, stream := range []io.Reader{out, errPipe} {
		go func() {
			defer relays.Done()
			relay(stream, tag, announce)
		}()
	}
	go func() {
		// Every line first: Wait closes the pipes, so reading has to be finished.
		relays.Wait()
		_ = cmd.Wait()
		close(c.done)
		g.once.Do(func() { close(g.exited) })
	}()
	return nil
}

// relay prefixes every line, and reports the daemon saying it is up.
func relay(r io.Reader, tag string, listening func()) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fmt.Println(tag + line)
		if strings.Contains(line, "listening") {
			listening()
		}
	}
}

// stop asks every child's process group to go, then insists. SIGTERM is advisory once a
// child installs its own handler, and one that ignores it would otherwise keep the whole
// stack alive.
func (g *group) stop() {
	g.mu.Lock()
	children := append([]*child(nil), g.children...)
	g.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range children {
		if c.cmd.Process == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pgid := -c.cmd.Process.Pid
			_ = syscall.Kill(pgid, syscall.SIGTERM)
			select {
			case <-c.done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(pgid, syscall.SIGKILL)
				<-c.done
			}
		}()
	}
	wg.Wait()
}

// forceOnSecondSignal turns a second Ctrl-C into an exit, rather than queueing it behind
// a stop that is already stuck.
func forceOnSecondSignal(ctx context.Context, g *group) {
	<-ctx.Done()
	again := make(chan os.Signal, 1)
	signal.Notify(again, os.Interrupt, syscall.SIGTERM)
	<-again
	fmt.Println(paint(red, "\nForced exit — surviving children keep their ports and sandboxes."))
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.children {
		if c.cmd.Process != nil {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	os.Exit(1)
}

func repoRoot() (string, error) {
	// The module root is wherever go.mod is, walking up from where this was run.
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if dir == filepath.Dir(dir) {
			return "", errors.New("run this from inside the repository")
		}
	}
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func setDefault(env *[]string, key, value string) {
	if os.Getenv(key) == "" {
		*env = append(*env, key+"="+value)
	}
}

func paint(colour int, text string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", colour, text)
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", width-len(s))
}

func width(servers []server) int {
	w := 0
	for _, s := range servers {
		w = max(w, len(s.name))
	}
	return w
}

// boxed frames lines in a box.
func boxed(lines []string) string {
	w := 0
	for _, line := range lines {
		w = max(w, len([]rune(line)))
	}
	rule := strings.Repeat("═", w+2)
	out := []string{"", "╔" + rule + "╗"}
	for _, line := range lines {
		out = append(out, "║ "+line+strings.Repeat(" ", w-len([]rune(line)))+" ║")
	}
	return strings.Join(append(out, "╚"+rule+"╝", ""), "\n")
}
