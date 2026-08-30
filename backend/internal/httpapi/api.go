// Package httpapi exposes the JSON API, the MJPEG proxy and the frontend's
// static files on one mux.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"esp32cam-nvr/internal/camera"
	"esp32cam-nvr/internal/discovery"
	"esp32cam-nvr/internal/manager"
	"esp32cam-nvr/internal/store"
)

const snapshotTimeout = 10 * time.Second

// Server wires the stores and background workers to HTTP handlers.
type Server struct {
	store      *store.Store
	manager    *manager.Manager
	discoverer *discovery.Discoverer
	static     http.Handler
}

// New builds the handler tree. staticDir may be empty, in which case only the
// API is served.
func New(st *store.Store, mgr *manager.Manager, disc *discovery.Discoverer, staticDir string) *Server {
	s := &Server{store: st, manager: mgr, discoverer: disc}
	if staticDir != "" {
		s.static = spaHandler(staticDir)
	}
	return s
}

// Handler returns the root mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/cameras", s.listCameras)
	mux.HandleFunc("POST /api/cameras", s.addCamera)
	mux.HandleFunc("DELETE /api/cameras/{id}", s.deleteCamera)
	mux.HandleFunc("GET /api/cameras/{id}/status", s.cameraStatus)
	mux.HandleFunc("GET /api/cameras/{id}/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/cameras/{id}/stream", s.stream)
	mux.HandleFunc("GET /api/discovered", s.discovered)

	if s.static != nil {
		mux.Handle("/", s.static)
	} else {
		// Without a static directory an unknown path is a mistake worth
		// reporting rather than an empty 200.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
	return logRequests(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"cameras": len(s.store.List()),
	})
}

func (s *Server) listCameras(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.manager.Views(s.store.List()))
}

type addRequest struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	User    string `json:"user"`
	Pass    string `json:"pass"`
}

func (s *Server) addCamera(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	// Bounded so a stray large body cannot be read into memory.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	// A pasted URL is the obvious mistake to make here, so accept one.
	req.Address = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(req.Address, "http://"), "https://"), "/")

	cam, err := s.store.Add(store.Camera{
		Address: req.Address,
		Name:    strings.TrimSpace(req.Name),
		User:    req.User,
		Pass:    req.Pass,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.manager.Sync(s.store.List())
	writeJSON(w, http.StatusCreated, cam.Public())
}

func (s *Server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	err := s.store.Delete(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.manager.Sync(s.store.List())
	w.WriteHeader(http.StatusNoContent)
}

// cameraStatus answers from the poller's cache. It never touches the camera,
// so any number of open browser tabs cost the device nothing.
func (s *Server) cameraStatus(w http.ResponseWriter, r *http.Request) {
	st, ok := s.manager.Status(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Get(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	ctx, cancel := contextWithTimeout(r, snapshotTimeout)
	defer cancel()

	img, err := s.manager.Snapshot(ctx, r.PathValue("id"))
	if err != nil {
		if ctx.Err() != nil {
			// The client gave up, so there is nobody left to answer.
			return
		}
		writeError(w, http.StatusBadGateway, fmt.Sprintf("snapshot failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(img)
}

// stream re-serves the camera's MJPEG with our session attached. The browser
// cannot fetch the camera directly because the sid cookie is scoped to the
// camera's own host, which is why this proxy exists at all.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Get(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	frames, unsubscribe, ok := s.manager.Stream(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	// Unsubscribing on every exit path is what stops a closed tab from leaving
	// the upstream connection to the camera running.
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "multipart/x-mixed-replace; boundary="+camera.Boundary)
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	rc.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case frame, open := <-frames:
			if !open {
				return
			}
			// Writing the length lets browsers decode each part without
			// scanning for the next boundary.
			part := fmt.Sprintf("--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", camera.Boundary, len(frame))
			if _, err := w.Write([]byte(part)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			// Without an explicit flush the frames sit in Go's buffer and the
			// viewer sees nothing until it fills.
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *Server) discovered(w http.ResponseWriter, r *http.Request) {
	cams := s.store.List()
	known := make([]string, 0, len(cams))
	for _, c := range cams {
		known = append(known, c.Address)
	}
	hosts := s.discoverer.Unconfigured(known)
	if hosts == nil {
		hosts = []discovery.Host{}
	}
	writeJSON(w, http.StatusOK, hosts)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// Streams run for minutes, so their duration is the viewing time.
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
