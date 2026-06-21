package realbackend

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// DockerAvailability is the result of probing for a working Docker
// engine. Phase 13+ backends that need Docker (Cloud Run/Functions real
// execution) call DetectDocker before trying to start a container and
// fall back to shape-only — never failing the request — when
// Available is false.
type DockerAvailability struct {
	// Available reports whether `docker` is on PATH and its daemon
	// responded to a version probe.
	Available bool
	// Detail is human-readable: the daemon's server version when
	// Available is true, or the reason it's false otherwise. Always
	// non-empty.
	Detail string
}

// DetectDocker probes for Docker without the Docker Go SDK (no new
// dependency, per this project's existing convention): it shells out to
// `docker version`, the same binary a real Docker install already puts
// on PATH. Any failure — binary missing, daemon not running, timeout —
// is reported via Available=false/Detail, never as a Go error the caller
// must handle, so this is always safe to call speculatively at startup
// or per opt-in request.
func DetectDocker(ctx context.Context) DockerAvailability {
	if _, err := exec.LookPath("docker"); err != nil {
		return DockerAvailability{Available: false, Detail: "docker binary not found on PATH"}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return DockerAvailability{Available: false, Detail: "docker daemon not reachable: " + err.Error()}
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return DockerAvailability{Available: false, Detail: "docker daemon returned an empty version"}
	}
	return DockerAvailability{Available: true, Detail: version}
}
