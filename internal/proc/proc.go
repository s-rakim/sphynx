// Package proc supervises long-running child processes (Headscale, tailscaled)
// and keeps their recent output in memory so the web UI can show logs.
package proc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Ring is a fixed-size buffer of log lines.
type Ring struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

func NewRing(n int) *Ring { return &Ring{lines: make([]string, n)} }

func (r *Ring) Add(s string) {
	r.mu.Lock()
	r.lines[r.next] = s
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Lines returns up to n of the most recent lines, oldest first.
func (r *Ring) Lines(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []string
	if r.full {
		all = append(all, r.lines[r.next:]...)
	}
	all = append(all, r.lines[:r.next]...)
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Writer returns an io.Writer that splits output into lines, adding each to
// the ring and echoing it to stderr with a prefix. Lines for which keep
// returns false are dropped (keep may be nil).
func (r *Ring) Writer(prefix string, keep func(string) bool) io.WriteCloser {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if keep != nil && !keep(line) {
				continue
			}
			r.Add(time.Now().Format("15:04:05 ") + line)
			fmt.Fprintf(os.Stderr, "[%s] %s\n", prefix, line)
		}
	}()
	return pw
}

// Process restarts its command whenever it exits, until stopped.
type Process struct {
	Name string
	Args func() []string // re-evaluated on each start
	// Keep filters noisy output lines; nil keeps everything.
	Keep func(line string) bool
	Env  []string
	Logs *Ring

	mu      sync.Mutex
	cmd     *exec.Cmd
	running bool
	started time.Time
	restart chan struct{}
	stopped bool
	exits   int
	lastErr string
}

func New(name string, args func() []string, env []string) *Process {
	return &Process{
		Name:    name,
		Args:    args,
		Env:     env,
		Logs:    NewRing(2000),
		restart: make(chan struct{}, 1),
	}
}

// Run blocks, keeping the process alive until ctx is cancelled.
func (p *Process) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		args := p.Args()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), p.Env...)
		w := p.Logs.Writer(p.Name, p.Keep)
		cmd.Stdout, cmd.Stderr = w, w
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		start := time.Now()
		if err := cmd.Start(); err != nil {
			p.Logs.Add("sphynx: failed to start: " + err.Error())
			log.Printf("%s: start: %v", p.Name, err)
			w.Close()
			p.wait(ctx, backoff)
			continue
		}
		p.mu.Lock()
		p.cmd, p.running, p.started = cmd, true, start
		p.mu.Unlock()

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		var err error
		select {
		case err = <-done:
		case <-p.restart:
			p.signal(cmd, syscall.SIGTERM)
			err = waitOrKill(cmd, done)
			backoff = 0
		case <-ctx.Done():
			p.signal(cmd, syscall.SIGTERM)
			waitOrKill(cmd, done)
		}
		w.Close()
		p.mu.Lock()
		p.running = false
		if backoff != 0 {
			p.exits++
		}
		if err != nil {
			p.lastErr = err.Error()
		}
		p.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if backoff == 0 {
			backoff = time.Second
			continue
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		p.Logs.Add(fmt.Sprintf("sphynx: %s exited (%v), restarting in %s", p.Name, err, backoff))
		p.wait(ctx, backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (p *Process) wait(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	case <-p.restart:
	}
}

func (p *Process) signal(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

func waitOrKill(cmd *exec.Cmd, done chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return <-done
	}
}

// Restart stops the running process; Run starts it again immediately.
func (p *Process) Restart() {
	select {
	case p.restart <- struct{}{}:
	default:
	}
}

// Signal sends sig to the running process (e.g. SIGHUP to reload).
func (p *Process) Signal(sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running || p.cmd == nil || p.cmd.Process == nil {
		return fmt.Errorf("%s is not running", p.Name)
	}
	return p.cmd.Process.Signal(sig)
}

type Status struct {
	Name      string    `json:"name"`
	Running   bool      `json:"running"`
	StartedAt time.Time `json:"startedAt"`
	Crashes   int       `json:"crashes"`
	LastError string    `json:"lastError,omitempty"`
}

func (p *Process) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Name: p.Name, Running: p.running, StartedAt: p.started, Crashes: p.exits, LastError: p.lastErr}
}
