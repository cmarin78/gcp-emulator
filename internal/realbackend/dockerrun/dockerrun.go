// Package dockerrun is Phase 14's real backend for Cloud Run services/jobs
// and Cloud Functions that opt into real execution
// (internal/realbackend.WantsReal): it actually `docker run`s the
// caller-supplied container image, the #1 committed real-execution item in
// ROADMAP.md ("does my image actually start and respond?").
//
// Like Phase 12's DetectDocker and Phase 13's embedded Postgres, this
// shells out rather than adding a Docker Go SDK dependency, per this
// project's "duplicate small helpers, avoid new deps" convention: every
// operation here is a `docker` CLI invocation via os/exec.
//
// Two shapes are exposed, matching the two ways a real container is used
// elsewhere in this codebase:
//
//   - Backend (via Start) is a long-running container — used by Cloud Run
//     services and (via a new emulator-only extension field, since the
//     real API has no image field) Cloud Functions — and satisfies
//     internal/realbackend.Backend, so it can be admitted/evicted by the
//     budget-aware Governor introduced in Phase 12.
//   - RunToCompletion is a one-shot, synchronous container run — used by
//     Cloud Run Jobs' manual ":run" action, which is a single batch task,
//     not a request-serving resource, so it's never admitted into the
//     Governor at all.
package dockerrun

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// FootprintMB is a conservative, fixed estimate of one running container's
// resident memory, used by realbackend.Governor for budget admission. Real
// usage varies wildly by image; this deliberately errs high rather than
// risk over-committing the host, the same stance Phase 13's embedded
// Postgres FootprintMB takes.
const FootprintMB = 256

// defaultReadyTimeout bounds how long Start waits for a freshly started
// container's published port to start accepting TCP connections before
// giving up and reporting a startup failure.
const defaultReadyTimeout = 20 * time.Second

// DefaultRunTimeout bounds RunToCompletion when the caller has no better
// timeout of its own (e.g. no parseable job-level timeout).
const DefaultRunTimeout = 300 * time.Second

// maxOutputBytes caps how much combined stdout+stderr RunToCompletion
// keeps, so a chatty container can't bloat the emulator's response/memory.
const maxOutputBytes = 8192

// Backend wraps one long-running container started with `docker run -d`.
// Implements internal/realbackend.Backend.
type Backend struct {
	containerID string
	hostPort    int
}

// Start starts image detached, publishing containerPort on a free local
// host port, with the given environment variables. It blocks until the
// published port accepts TCP connections or readyTimeout elapses (use 0
// for defaultReadyTimeout), so a caller never gets back a Backend that
// isn't actually reachable yet — the same "verify it actually works"
// posture as Phase 13's embedded Postgres Start.
func Start(image string, env map[string]string, containerPort int, readyTimeout time.Duration) (*Backend, error) {
	if readyTimeout <= 0 {
		readyTimeout = defaultReadyTimeout
	}
	hostPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("no se pudo reservar un puerto local: %w", err)
	}
	args := []string{"run", "-d", "--rm", "-p", fmt.Sprintf("%d:%d", hostPort, containerPort)}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, image)
	// runStartTimeout bounds the `docker run -d` invocation itself
	// (separate from readyTimeout's port-polling below): a missing/
	// unpullable image must fail in bounded time, not hang the request
	// that triggered it indefinitely.
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(startCtx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker run falló para %q: %w", image, err)
	}
	containerID := strings.TrimSpace(string(out))
	b := &Backend{containerID: containerID, hostPort: hostPort}
	if err := waitForPort(hostPort, readyTimeout); err != nil {
		_ = b.Stop()
		return nil, fmt.Errorf("el contenedor (imagen %q) no respondió en el puerto %d dentro de %s: %w", image, hostPort, readyTimeout, err)
	}
	return b, nil
}

// Kind identifies this backend flavor for realbackend.Governor's
// introspection endpoint.
func (b *Backend) Kind() string { return "docker-container" }

// FootprintMB implements internal/realbackend.Backend.
func (b *Backend) FootprintMB() int { return FootprintMB }

// Stop stops (and, since Start uses --rm, implicitly removes) the
// container. Safe to call on a Backend that failed to fully start, or on
// a nil Backend.
func (b *Backend) Stop() error {
	if b == nil || b.containerID == "" {
		return nil
	}
	return exec.Command("docker", "stop", b.containerID).Run()
}

// Host is always local: containers are published to the loopback
// interface only, never exposed beyond this machine.
func (b *Backend) Host() string { return "127.0.0.1" }

// Port is the local host port mapped to the container's published port.
func (b *Backend) Port() int { return b.hostPort }

// URL is the locally reachable HTTP endpoint fronting the container.
func (b *Backend) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", b.hostPort) }

// ContainerID is the docker-assigned container ID, exposed only for
// logging/diagnostics.
func (b *Backend) ContainerID() string { return b.containerID }

func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	return lastErr
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// RunToCompletion runs image once, synchronously, to completion — for
// Cloud Run Jobs' ":run" action, a one-shot task rather than a
// long-running service, so it's never admitted into the Governor. It
// captures combined stdout+stderr (truncated to maxOutputBytes) and the
// container's exit code.
//
// A non-zero exit code is reported via exitCode, not a Go error — only a
// Docker-level failure (image not found, daemon unreachable, the timeout
// elapsing) is returned as err, mirroring how a real Cloud Run Jobs
// execution distinguishes "the task ran and failed" from "the task never
// ran at all."
func RunToCompletion(image string, env map[string]string, timeout time.Duration) (exitCode int, output string, err error) {
	if timeout <= 0 {
		timeout = DefaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"run", "--rm"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, image)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	output = truncate(buf.String(), maxOutputBytes)

	if runErr == nil {
		return 0, output, nil
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), output, nil
	}
	return -1, output, fmt.Errorf("docker run falló para %q: %w", image, runErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
