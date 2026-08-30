package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/avi"
	"argus-nvr/internal/discovery"
	"argus-nvr/internal/manager"
	"argus-nvr/internal/store"
)

func aviOf(t *testing.T, frames int) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.avi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w, err := avi.NewWriter(f)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	frame := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x01, 0xE0, 0x02, 0x80, 0x03,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xD9}
	at := time.Unix(1700000000, 0)
	for i := 0; i < frames; i++ {
		w.WriteFrame(frame, at)
		at = at.Add(40 * time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

func serverWithRecordings(t *testing.T) (http.Handler, *archive.Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	arch, err := archive.Open(filepath.Join(dir, "recordings"), 0)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	clip := aviOf(t, 10)
	for _, id := range []archive.ID{
		{CameraID: "a1b2", Day: "2026-08-29", At: "090000"},
		{CameraID: "a1b2", Day: "2026-08-30", At: "131529"},
		{CameraID: "c3d4", Day: "2026-08-30", At: "144901"},
	} {
		if _, err := arch.Save(id, bytes.NewReader(clip), archive.Meta{
			Source: archive.SourceCamera, DurMs: 10004, Frames: 213,
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	st, err := store.Open(filepath.Join(dir, "cameras.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	api := New(st, manager.New(nil), discovery.New(), arch, "")
	return api.Handler(), arch, clip
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestListingWhatTheServiceHolds(t *testing.T) {
	h, _, _ := serverWithRecordings(t)

	w := get(t, h, "/api/recordings")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var doc struct {
		Recordings []archive.Recording `json:"recordings"`
		More       bool                `json:"more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body)
	}
	if len(doc.Recordings) != 3 || doc.More {
		t.Fatalf("listed %d recordings more=%v", len(doc.Recordings), doc.More)
	}
	if doc.Recordings[0].Day != "2026-08-30" {
		t.Errorf("not newest first: %+v", doc.Recordings)
	}

	w = get(t, h, "/api/recordings?cameraId=a1b2")
	json.Unmarshal(w.Body.Bytes(), &doc)
	if len(doc.Recordings) != 2 {
		t.Errorf("one camera has %d recordings, wanted 2", len(doc.Recordings))
	}

	w = get(t, h, "/api/recordings?day=2026-08-30")
	json.Unmarshal(w.Body.Bytes(), &doc)
	if len(doc.Recordings) != 2 {
		t.Errorf("one day has %d recordings, wanted 2", len(doc.Recordings))
	}

	w = get(t, h, "/api/recordings?limit=1")
	json.Unmarshal(w.Body.Bytes(), &doc)
	if len(doc.Recordings) != 1 || !doc.More {
		t.Errorf("a cut-short listing did not say so: %d more=%v", len(doc.Recordings), doc.More)
	}

	if w := get(t, h, "/api/recordings?day=yesterday"); w.Code != http.StatusBadRequest {
		t.Errorf("a day that is not a date gave %d", w.Code)
	}
}

func TestListingDays(t *testing.T) {
	h, _, _ := serverWithRecordings(t)

	w := get(t, h, "/api/recordings/days")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var doc struct {
		Days []archive.Day `json:"days"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Days) != 3 {
		t.Fatalf("summarised %d camera-days, wanted 3: %+v", len(doc.Days), doc.Days)
	}
	if doc.Days[0].Day != "2026-08-30" || doc.Days[0].Bytes == 0 {
		t.Errorf("day summary is wrong: %+v", doc.Days[0])
	}
}

func TestFetchingARecording(t *testing.T) {
	h, _, clip := serverWithRecordings(t)

	for _, path := range []string{
		"/api/recordings/a1b2/2026-08-30/131529",
		"/api/recordings/a1b2/2026-08-30/131529.avi",
	} {
		w := get(t, h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != "video/x-msvideo" {
			t.Errorf("%s: served as %q", path, got)
		}
		if !bytes.Equal(w.Body.Bytes(), clip) {
			t.Errorf("%s: served %d bytes, the recording is %d", path, w.Body.Len(), len(clip))
		}
	}

	// A player seeking in a clip asks for the middle of the file, so ranges
	// have to work or every scrub downloads the whole thing again.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/recordings/a1b2/2026-08-30/131529", nil)
	r.Header.Set("Range", "bytes=0-99")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent {
		t.Errorf("a range request gave %d", w.Code)
	}
	if w.Body.Len() != 100 {
		t.Errorf("a range request gave %d bytes", w.Body.Len())
	}

	if w := get(t, h, "/api/recordings/a1b2/2026-08-30/000001"); w.Code != http.StatusNotFound {
		t.Errorf("a recording that is not held gave %d", w.Code)
	}
	if w := get(t, h, "/api/recordings/a1b2/nonsense/131529"); w.Code != http.StatusNotFound {
		t.Errorf("a day that is not a date gave %d", w.Code)
	}
}

func TestStorageReportsWhatRetentionIsDeciding(t *testing.T) {
	h, _, clip := serverWithRecordings(t)

	w := get(t, h, "/api/storage")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var usage archive.Usage
	if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if usage.Recordings != 3 {
		t.Errorf("counted %d recordings, wanted 3", usage.Recordings)
	}
	if usage.Bytes < int64(len(clip))*3 {
		t.Errorf("%d bytes for three %d byte recordings", usage.Bytes, len(clip))
	}
}

// A service with nowhere to keep recordings says so rather than answering as
// though it holds none.
func TestWithoutAnArchive(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cameras.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	h := New(st, manager.New(nil), discovery.New(), nil, "").Handler()
	for _, path := range []string{"/api/recordings", "/api/recordings/days", "/api/storage"} {
		if w := get(t, h, path); w.Code != http.StatusNotFound {
			t.Errorf("%s gave %d", path, w.Code)
		}
	}
}

// A camera password is the one thing that must never leave this service, in a
// response or in a log line.
func TestCameraPasswordsNeverLeave(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "cameras.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const secret = "not-a-real-password"
	if _, err := st.Add(store.Camera{Address: "192.0.2.10", Name: "alpha", User: "admin", Pass: secret}); err != nil {
		t.Fatalf("add camera: %v", err)
	}
	arch, err := archive.Open(filepath.Join(dir, "recordings"), 0)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	h := New(st, manager.New(arch), discovery.New(), arch, "").Handler()

	for _, path := range []string{"/api/cameras", "/api/recordings", "/api/recordings/days", "/api/storage", "/healthz"} {
		w := get(t, h, path)
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("%s answered with the camera's password", path)
		}
	}
}
