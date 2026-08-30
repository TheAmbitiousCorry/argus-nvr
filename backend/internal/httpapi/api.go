// Package httpapi exposes the JSON API, the MJPEG proxy and the frontend's
// static files on one mux.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/camera"
	"argus-nvr/internal/discovery"
	"argus-nvr/internal/manager"
	"argus-nvr/internal/store"
)

const snapshotTimeout = 10 * time.Second

// configTimeout allows for a read that first has to log in again, which costs
// two round trips to a device that answers slowly when it is busy.
const configTimeout = 25 * time.Second

// maxFirmwareUpload is well clear of the roughly 1.4MB images these cameras
// take, while keeping a mistaken upload from being read into memory.
const maxFirmwareUpload = 8 << 20

// Server wires the stores and background workers to HTTP handlers.
type Server struct {
	store      *store.Store
	manager    *manager.Manager
	discoverer *discovery.Discoverer
	archive    *archive.Store
	static     http.Handler
}

// New builds the handler tree. staticDir may be empty, in which case only the
// API is served, and arch may be nil, in which case the service holds no
// recordings and says so rather than pretending to hold none.
func New(st *store.Store, mgr *manager.Manager, disc *discovery.Discoverer, arch *archive.Store, staticDir string) *Server {
	s := &Server{store: st, manager: mgr, discoverer: disc, archive: arch}
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
	mux.HandleFunc("GET /api/cameras/{id}/config", s.cameraConfig)
	mux.HandleFunc("GET /api/cameras/{id}/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/cameras/{id}/stream", s.stream)
	mux.HandleFunc("GET /api/discovered", s.discovered)
	mux.HandleFunc("GET /api/recordings", s.listRecordings)
	mux.HandleFunc("GET /api/recordings/days", s.recordingDays)
	mux.HandleFunc("GET /api/recordings/{cameraId}/{day}/{at}", s.recording)
	mux.HandleFunc("GET /api/recordings/{cameraId}/{day}/{at}/frames", s.recordingFrames)
	mux.HandleFunc("GET /api/recordings/{cameraId}/{day}/{at}/stream", s.recordingStream)
	mux.HandleFunc("GET /api/storage", s.storage)
	mux.HandleFunc("POST /api/settings", s.bulkSettings)
	mux.HandleFunc("POST /api/firmware", s.bulkFirmware)

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

	name := strings.TrimSpace(req.Name)
	// A camera offered by discovery is usually added under its .local name,
	// which does not resolve in the container, so it is stored as the IP
	// discovery saw it at. The name it was added under is worth keeping as the
	// label, since an IP makes a poor one.
	if addr, moved := s.discoverer.ResolveAddress(req.Address); moved {
		if name == "" {
			name = req.Address
		}
		req.Address = addr
	}

	cam, err := s.store.Add(store.Camera{
		Address: req.Address,
		Name:    name,
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

// cameraConfig passes the camera's whole /config through untouched, so a
// setting a later firmware gains needs no change on this side.
func (s *Server) cameraConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, "no such camera")
		return
	}
	ctx, cancel := contextWithTimeout(r, configTimeout)
	defer cancel()

	cfg, err := s.manager.Config(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("camera did not answer: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(cfg)
}

// settingsRequest is a partial update for any number of cameras. The values
// stay as raw JSON so the field names, and only the field names, are this
// service's business.
type settingsRequest struct {
	CameraIDs []string                   `json:"cameraIds"`
	Image     map[string]json.RawMessage `json:"image"`
	Recording map[string]json.RawMessage `json:"recording"`
}

// bulkSettings answers 200 whatever the cameras did, with one result each: a
// camera that is offline is news about that camera, not a failed request.
func (s *Server) bulkSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	results := s.manager.ApplySettings(r.Context(), req.CameraIDs, camera.Settings{
		Image:     req.Image,
		Recording: req.Recording,
	})
	writeResults(w, results)
}

// bulkFirmware relays one uploaded image to each camera in turn. The image is
// read into memory rather than streamed because each camera needs its own copy
// of the bytes and the firmware needs a Content-Length up front.
func (s *Server) bulkFirmware(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFirmwareUpload)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with a file and cameraIds")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	image, err := io.ReadAll(io.LimitReader(file, maxFirmwareUpload))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the uploaded file")
		return
	}
	if len(image) == 0 {
		writeError(w, http.StatusBadRequest, "the uploaded file is empty")
		return
	}

	writeResults(w, s.manager.Flash(r.Context(), cameraIDs(r.Form["cameraIds"]), image))
}

// cameraIDs reads the comma-separated list, tolerating the field being repeated
// rather than joined, which is what a form builder is as likely to produce.
func cameraIDs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func writeResults(w http.ResponseWriter, results []manager.Result) {
	if results == nil {
		results = []manager.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
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
