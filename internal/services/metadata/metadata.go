// Package metadata implements the Google Compute Engine (GCE) Metadata Server
// (http://metadata.google.internal/computeMetadata/v1/ or 169.254.169.254).
// Google Cloud client libraries, Terraform, and container runtimes query this
// service to discover project IDs, instance zones, and OAuth2 access tokens.
package metadata

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	headerMetadataFlavor = "Metadata-Flavor"
	expectedFlavor       = "Google"
	defaultProject       = "demo-project"
	defaultNumericProj   = "1000000000001"
	defaultZone          = "projects/1000000000001/zones/us-central1-a"
	defaultInstanceID    = "1000000000000000001"
)

type Svc struct {
	db *storage.DB
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

func (s *Svc) Register(mux *http.ServeMux) {
	// Base and recursive endpoints
	mux.HandleFunc("GET /computeMetadata/v1", s.handleRoot)
	mux.HandleFunc("GET /computeMetadata/v1/", s.handleRoot)

	// Project metadata
	mux.HandleFunc("GET /computeMetadata/v1/project/project-id", s.handleProjectID)
	mux.HandleFunc("GET /computeMetadata/v1/project/numeric-project-id", s.handleNumericProjectID)

	// Instance metadata
	mux.HandleFunc("GET /computeMetadata/v1/instance/id", s.handleInstanceID)
	mux.HandleFunc("GET /computeMetadata/v1/instance/name", s.handleInstanceName)
	mux.HandleFunc("GET /computeMetadata/v1/instance/zone", s.handleInstanceZone)
	mux.HandleFunc("GET /computeMetadata/v1/instance/hostname", s.handleHostname)

	// Service Accounts
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/", s.handleListServiceAccounts)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/", s.handleAccountDir)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/token", s.handleAccountToken)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/email", s.handleAccountEmail)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/identity", s.handleAccountIdentity)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/scopes", s.handleAccountScopes)
	mux.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{account}/aliases", s.handleAccountAliases)
}

func checkFlavor(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(headerMetadataFlavor) != expectedFlavor {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Missing required metadata-flavor header: Google"))
		return false
	}
	return true
}

func (s *Svc) handleRoot(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "instance/\nproject/")
}

func (s *Svc) handleProjectID(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(defaultProject))
}

func (s *Svc) handleNumericProjectID(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(defaultNumericProj))
}

func (s *Svc) handleInstanceID(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(defaultInstanceID))
}

func (s *Svc) handleInstanceName(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("gcp-emulator-instance"))
}

func (s *Svc) handleInstanceZone(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(defaultZone))
}

func (s *Svc) handleHostname(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("gcp-emulator-instance.c.demo-project.internal"))
}

func (s *Svc) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "default/\ndefault@developer.gserviceaccount.com/")
}

func (s *Svc) handleAccountDir(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "aliases\nemail\nidentity\nscopes\ntoken")
}

func (s *Svc) handleAccountToken(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	account := r.PathValue("account")
	if account == "" {
		account = "default"
	}
	token := map[string]any{
		"access_token": fmt.Sprintf("ya29.emulator-%s-%d", strings.ReplaceAll(account, "@", "-"), time.Now().Unix()),
		"expires_in":   3600,
		"token_type":   "Bearer",
	}
	server.WriteJSON(w, 200, token)
}

func (s *Svc) handleAccountEmail(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	account := r.PathValue("account")
	email := "default@developer.gserviceaccount.com"
	if account != "default" && strings.Contains(account, "@") {
		email = account
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(email))
}

func (s *Svc) handleAccountIdentity(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	// OpenID Connect identity token
	jwt := "eyJhbGciOiJSUzI1NiIsImtpZCI6ImVtdWxhdG9yIn0.eyJpc3MiOiJodHRwczovL2FjY291bnRzLmdvb2dsZS5jb20iLCJzdWIiOiIxMDAwMDAwMDAwMDEiLCJhdWQiOiJlbXVsYXRvciIsImF6cCI6ImVtdWxhdG9yIiwiZW1haWwiOiJkZWZhdWx0QGRldmVsb3Blci5nc2VydmljZWFjY291bnQuY29tIiwiZW1haWxfdmVyaWZpZWQiOnRydWUsImV4cCI6OTk5OTk5OTk5OSwiaWF0IjoxNjAwMDAwMDAwfQ.c2lnbmF0dXJl"
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(jwt))
}

func (s *Svc) handleAccountScopes(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "https://www.googleapis.com/auth/cloud-platform")
}

func (s *Svc) handleAccountAliases(w http.ResponseWriter, r *http.Request) {
	if !checkFlavor(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "default")
}
