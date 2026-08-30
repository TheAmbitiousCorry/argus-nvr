package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"argus-nvr/internal/archive"
)

// defaultLimit keeps a listing to something a page can hold. A camera on a busy
// day makes hundreds of recordings and a fleet makes thousands, and the caller
// that wants all of them can page through with start.
const defaultLimit = 200

// maxLimit bounds what one request can ask for.
const maxLimit = 1000

// listRecordings answers what the service holds, newest first.
//
// The shape follows the camera's own day listing: a list, and a `more` flag
// saying the answer was cut short, paged with `start`. Two sides counting
// recordings the same way is worth more here than a cleverer cursor.
func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return
	}
	q := r.URL.Query()
	f := archive.Filter{
		CameraID: q.Get("cameraId"),
		Day:      q.Get("day"),
		Start:    atoiDefault(q.Get("start"), 0),
		Limit:    atoiDefault(q.Get("limit"), defaultLimit),
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Day != "" && !archive.ValidDay(f.Day) {
		writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
		return
	}

	recs, more, err := s.archive.List(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if recs == nil {
		recs = []archive.Recording{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recs,
		"start":      f.Start,
		"more":       more,
	})
}

// recordingDays answers which days hold anything, so a caller can offer a date
// without first fetching every recording.
func (s *Server) recordingDays(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return
	}
	days, err := s.archive.Days(r.URL.Query().Get("cameraId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if days == nil {
		days = []archive.Day{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

// recording serves one AVI off the data volume.
//
// http.ServeContent rather than a copy, because it answers range requests: a
// video element seeking in a clip asks for the middle of the file, and without
// ranges it downloads the whole thing again for every scrub.
func (s *Server) recording(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return
	}
	id := archive.ID{
		CameraID: r.PathValue("cameraId"),
		Day:      r.PathValue("day"),
		// The extension is optional, so the same URL can be pasted into a
		// player that decides what to do from the name.
		At: strings.TrimSuffix(r.PathValue("at"), ".avi"),
	}

	f, info, err := s.archive.Open(id)
	if errors.Is(err, archive.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	name := id.CameraID + "-" + id.Day + "-" + id.At + ".avi"
	w.Header().Set("Content-Type", "video/x-msvideo")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	// A recording never changes once it is held, so it is worth caching hard.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// storage reports what the archive is using against what it is allowed, which
// is the number that decides when the oldest recordings start going.
func (s *Server) storage(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return
	}
	usage, err := s.archive.Usage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
