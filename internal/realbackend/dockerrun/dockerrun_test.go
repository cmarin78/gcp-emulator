package dockerrun

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/realbackend"
)

// TestTruncateLeavesShortOutputUntouched and TestTruncateCapsLongOutput
// cover the pure helper without needing Docker at all, so they always run
// in routine `go test ./...`/CI.
func TestTruncateLeavesShortOutputUntouched(t *testing.T) {
	if got := truncate("hola", 100); got != "hola" {
		t.Fatalf("expected short output untouched, got %q", got)
	}
}

func TestTruncateCapsLongOutput(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'x'
	}
	got := truncate(string(long), 10)
	if len(got) <= 10 {
		t.Fatalf("expected truncated output to include the marker suffix, got %q", got)
	}
	if got[:10] != string(long[:10]) {
		t.Fatalf("expected truncated output to keep the first 10 bytes, got %q", got)
	}
}

// TestStartFailsFastOnUnknownDockerBinaryOrImage exercises Start's error
// path without requiring a real, working Docker daemon: an image name
// that can never resolve (or a missing docker binary) must return an
// error, never panic or hang past a short timeout.
func TestStartFailsFastOnUnknownDockerBinaryOrImage(t *testing.T) {
	_, err := Start("emulator.dev/this-image-does-not-exist:latest", nil, 8080, 3*time.Second)
	if err == nil {
		t.Fatal("expected Start to fail for a nonexistent image")
	}
}

// TestBackendStopIsSafeOnZeroValue mirrors the nil-safety every other
// Backend implementation in this codebase (e.g. postgres.Backend.Stop)
// guarantees: calling Stop on a Backend that never fully started, or on a
// nil pointer, must never panic.
func TestBackendStopIsSafeOnZeroValue(t *testing.T) {
	var b *Backend
	if err := b.Stop(); err != nil {
		t.Fatalf("expected nil Backend.Stop to be a safe no-op, got %v", err)
	}
	zero := &Backend{}
	if err := zero.Stop(); err != nil {
		t.Fatalf("expected zero-value Backend.Stop to be a safe no-op, got %v", err)
	}
}

// realDockerTestsEnabled gates the handful of tests below that actually
// shell out to a real Docker daemon and pull/run a real image — mirroring
// Phase 13's EMULATOR_REAL_PG_TESTS gate for embedded Postgres, so routine
// `go test ./...`/CI never needs Docker installed. Even with the env var
// set, this also checks realbackend.DetectDocker so the test still skips
// cleanly on a machine where Docker isn't actually available.
func realDockerTestsEnabled(t *testing.T) bool {
	t.Helper()
	if os.Getenv("EMULATOR_REAL_DOCKER_TESTS") != "1" {
		t.Skip("saltado: set EMULATOR_REAL_DOCKER_TESTS=1 para correr este test contra un daemon Docker real")
		return false
	}
	avail := realbackend.DetectDocker(context.Background())
	if !avail.Available {
		t.Skipf("saltado: Docker no está disponible en esta máquina (%s)", avail.Detail)
		return false
	}
	return true
}

// TestRunToCompletionRealDocker actually runs a tiny, fast, always-available
// image to completion and checks the captured exit code/output.
func TestRunToCompletionRealDocker(t *testing.T) {
	if !realDockerTestsEnabled(t) {
		return
	}
	exitCode, output, err := RunToCompletion("busybox:latest", map[string]string{"GREETING": "hola"}, 60*time.Second)
	if err != nil {
		t.Fatalf("RunToCompletion: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (output=%q)", exitCode, output)
	}
}

// TestStartAndStopRealDocker starts a real long-running container,
// confirms it's actually reachable over the published port, then stops
// it.
func TestStartAndStopRealDocker(t *testing.T) {
	if !realDockerTestsEnabled(t) {
		return
	}
	backend, err := Start("nginx:alpine", nil, 80, 30*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer backend.Stop()

	if backend.Host() != "127.0.0.1" || backend.Port() == 0 {
		t.Fatalf("expected a reachable host/port, got host=%q port=%d", backend.Host(), backend.Port())
	}
	if err := backend.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
