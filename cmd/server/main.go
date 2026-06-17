// Comando principal del emulador de GCP. Levanta un servidor HTTP que
// expone APIs compatibles con storage.googleapis.com, compute.googleapis.com
// e iam.googleapis.com, persistiendo todo en un único archivo embebido
// (BoltDB) para que el stack sea 100% portable: un solo binario + un
// archivo de datos, sin dependencias externas.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/services/compute"
	"github.com/cesar/gcp-emulator/internal/services/gcs"
	"github.com/cesar/gcp-emulator/internal/services/iam"
	"github.com/cesar/gcp-emulator/internal/storage"
)

func main() {
	addr := flag.String("addr", envOr("EMULATOR_ADDR", ":8443"), "dirección HTTP de escucha (host:puerto)")
	dbPath := flag.String("db", envOr("EMULATOR_DB", "data/emulator.db"), "ruta al archivo de datos embebido")
	staticDir := flag.String("web", envOr("EMULATOR_WEB", "web/console"), "directorio del frontend (consola)")
	flag.Parse()

	db, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos: %v", err)
	}
	defer db.Close()

	srv := server.New()
	mux := srv.Mux()

	iam.New(db).Register(mux)
	gcs.New(db).Register(mux)
	compute.New(db).Register(mux)

	// Endpoint de salud, útil para chequear que el emulador está arriba.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		server.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})

	// Frontend estático (consola tipo Google Cloud Console).
	if info, statErr := os.Stat(*staticDir); statErr == nil && info.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(*staticDir)))
	}

	log.Printf("GCP Emulator escuchando en %s (db=%s, web=%s)", *addr, *dbPath, *staticDir)
	log.Printf("Endpoints: /storage/v1/*  /compute/v1/*  /v1/* (IAM)  /healthz")
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
