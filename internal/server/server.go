// Package server arma el http.Server principal del emulador, montando las
// rutas de cada servicio bajo los mismos paths que usa la API real de GCP
// (p. ej. /storage/v1/b, /compute/v1/projects/...), de forma que el SDK de
// Google y el propio gcloud CLI puedan apuntar al emulador vía
// api_endpoint_overrides sin modificaciones.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Server agrupa el mux principal y permite registrar routers de servicios.
type Server struct {
	mux *http.ServeMux
}

func New() *Server {
	return &Server{mux: http.NewServeMux()}
}

// Mux expone el ServeMux subyacente para que cada servicio registre sus rutas.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// Handler devuelve el http.Handler final, con logging y CORS para que el
// frontend (consola web) pueda llamar al emulador desde otro puerto/origen.
func (s *Server) Handler() http.Handler {
	return withCORS(withLogging(s.mux))
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteJSON serializa v como JSON con el status code dado. Helper común
// para todos los handlers de servicios.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("error escribiendo respuesta JSON: %v", err)
	}
}

// APIError replica el formato de error estándar de las APIs de Google:
// {"error": {"code": 404, "message": "...", "status": "NOT_FOUND"}}
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func WriteError(w http.ResponseWriter, code int, status, message string) {
	WriteJSON(w, code, map[string]APIError{"error": {Code: code, Message: message, Status: status}})
}
