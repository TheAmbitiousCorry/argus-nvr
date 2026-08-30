package httpapi

import (
	"errors"
	"net/http"
	"strconv"

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

// contentTypes is what each form of a recording goes out as. A transcoded
// recording is served as MP4 from the same URL the AVI was served from, because
// the form is not part of the identity and nothing that already points at a
// recording should break when it is transcoded.
var contentTypes = map[string]string{
	archive.FormatAVI: "video/x-msvideo",
	archive.FormatMP4: "video/mp4",
}

// recording serves one recording off the data volume, in whichever form it is
// held in.
//
// http.ServeContent rather than a copy, because it answers range requests: a
// video element seeking in a clip asks for the middle of the file, and without
// ranges it downloads the whole thing again for every scrub. That matters far
// more now that a transcoded recording is played by a video element that seeks
// for real rather than replayed frame by frame.
func (s *Server) recording(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return
	}
	id := recordingID(r)

	f, info, format, err := s.archive.Open(id)
	if errors.Is(err, archive.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such recording")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	name := id.CameraID + "-" + id.Day + "-" + id.At + "." + format
	w.Header().Set("Content-Type", contentTypes[format])
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	// Cached, but revalidated every time. A recording used to be immutable and
	// this was cached for a day; transcoding changes the bytes at this URL
	// without changing the URL, and a browser holding the AVI it fetched this
	// morning would keep playing it through the replay long after the service
	// had an MP4 to offer. ServeContent answers a revalidation from the
	// modification time, so the cost is one 304 and the body is still not sent
	// twice.
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
