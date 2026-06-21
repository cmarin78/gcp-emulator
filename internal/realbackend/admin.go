package realbackend

import (
	"net/http"

	"github.com/cesar/gcp-emulator/internal/server"
)

// RegisterAdmin mounts GET /admin/real-backends, returning g's current
// budget usage and admitted backends — the roadmap's "small introspection
// endpoint... so the adaptive behavior is visible rather than a black
// box." Registered unconditionally at startup, even though no concrete
// Backend exists yet in this phase, so it's a stable place for
// dashboards/tests to point at from day one (it'll simply report zero
// backends until Phase 13+ adds a real one).
func RegisterAdmin(mux *http.ServeMux, g *Governor) {
	mux.HandleFunc("GET /admin/real-backends", func(w http.ResponseWriter, r *http.Request) {
		server.WriteJSON(w, 200, g.Snapshot())
	})
}
