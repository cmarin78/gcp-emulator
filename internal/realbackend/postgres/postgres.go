// Package postgres is Phase 13's real backend for Cloud SQL Postgres
// instances that opt into real execution (internal/realbackend.WantsReal).
// It satisfies internal/realbackend.Backend, so it can be admitted and
// evicted by the budget-aware Governor introduced in Phase 12.
//
// "Embedded" here means a real PostgreSQL server binary, downloaded once
// (and cached locally) by github.com/fergusstrange/embedded-postgres —
// not a reimplementation, and not a Docker container. This is the
// cheapest of the two committed real-execution items in ROADMAP.md
// precisely because it needs neither: the first run on a machine needs
// network access to fetch the binary; every run after that reuses the
// cached copy and works offline.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq"
)

// FootprintMB is a conservative, fixed estimate of a running embedded
// Postgres server's resident memory, used by realbackend.Governor for
// budget admission. Real usage varies with workload; this deliberately
// errs high rather than risk over-committing the host.
const FootprintMB = 200

// adminUser is the maintenance-connection username used internally to
// run CREATE DATABASE/CREATE ROLE statements. It is never returned to API
// callers as-is; the instance's RealConnection exposes a generated,
// instance-specific password alongside it instead.
const adminUser = "postgres"

// Backend wraps a single embedded PostgreSQL server process.
type Backend struct {
	engine   *embeddedpostgres.EmbeddedPostgres
	port     int
	password string
}

// Start launches a real embedded Postgres server on a free local TCP
// port and returns a Backend wrapping it. The caller is responsible for
// admitting it into a realbackend.Governor and eventually calling Stop
// (typically via Governor.Release/eviction) when the owning Cloud SQL
// instance is deleted or evicted.
func Start(password string) (*Backend, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("no se pudo reservar un puerto local: %w", err)
	}
	engine := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V15).
		Port(uint32(port)).
		Username(adminUser).
		Password(password).
		Database("postgres").
		StartTimeout(45 * time.Second).
		Logger(io.Discard))
	if err := engine.Start(); err != nil {
		return nil, fmt.Errorf("no se pudo iniciar postgres embebido: %w", err)
	}
	return &Backend{engine: engine, port: port, password: password}, nil
}

// Kind identifies this backend flavor for realbackend.Governor's
// introspection endpoint.
func (b *Backend) Kind() string { return "cloudsql-postgres-embedded" }

// FootprintMB implements realbackend.Backend.
func (b *Backend) FootprintMB() int { return FootprintMB }

// Stop shuts down the embedded server. Safe to call on a Backend that
// failed to fully start.
func (b *Backend) Stop() error {
	if b == nil || b.engine == nil {
		return nil
	}
	return b.engine.Stop()
}

// Host is always local: this is an embedded, single-machine engine, never
// exposed beyond the loopback interface.
func (b *Backend) Host() string { return "127.0.0.1" }

// Port is the local TCP port the embedded server is listening on.
func (b *Backend) Port() int { return b.port }

// dsn returns a maintenance connection string (database "postgres",
// admin user) suitable for CREATE DATABASE/CREATE ROLE statements, which
// in Postgres must run outside the database they're creating.
func (b *Backend) dsn() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		b.Host(), b.port, adminUser, b.password)
}

// Exec runs a single DDL statement (CREATE/DROP DATABASE, CREATE/DROP
// ROLE) against the maintenance connection. Callers are responsible for
// quoting identifiers/literals (see quoteIdent/quoteLiteral in the
// cloudsql package) — this is a local, single-tenant dev engine reachable
// only from this same machine, not a multi-tenant server exposed to
// untrusted input, so the bar here is correctness, not SQL-injection
// hardening against an external attacker.
func (b *Backend) Exec(query string) error {
	db, err := sql.Open("postgres", b.dsn())
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(query)
	return err
}

// Stats reports a live, instantaneous snapshot of this server's
// connection load by querying pg_stat_activity on the maintenance
// connection — Phase 15's source for real Cloud SQL metrics (replacing
// the always-empty stub). connections is the number of backend processes
// currently connected, across every database on this engine (this is a
// single-tenant-per-instance embedded server, so that's the right scope:
// there's no other Cloud SQL instance sharing it).
func (b *Backend) Stats(ctx context.Context) (connections int, err error) {
	if b == nil || b.engine == nil {
		return 0, fmt.Errorf("postgres: backend sin motor")
	}
	db, err := sql.Open("postgres", b.dsn())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity")
	if err := row.Scan(&connections); err != nil {
		return 0, fmt.Errorf("postgres: no se pudo leer pg_stat_activity: %w", err)
	}
	return connections, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
