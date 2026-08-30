package manager

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"argus-nvr/internal/store"
)

// versionCamera is a camera that answers the two routes the poll reads, and
// counts how often each was asked for.
type versionCamera struct {
	records  atomic.Int64
	versions atomic.Int64
	version  atomic.Value // string
	missing  atomic.Bool
}

func (c *versionCamera) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/record", func(w http.ResponseWriter, r *http.Request) {
		c.records.Add(1)
		io.WriteString(w, `{"active":false,"storage":"ok"}`)
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		c.versions.Add(1)
		if c.missing.Load() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, c.version.Load().(string))
	})
	return mux
}

func startCamera(t *testing.T, cam *versionCamera) (*Manager, store.Camera) {
	t.Helper()
	srv := httptest.NewServer(cam.handler())
	t.Cleanup(srv.Close)

	c := store.Camera{
		ID:      "cam1",
		Address: strings.TrimPrefix(srv.URL, "http://"),
		Name:    "alpha",
		User:    "admin",
		Pass:    "secret",
	}
	m := New(nil, nil)
	m.Sync([]store.Camera{c})
	t.Cleanup(m.Close)
	return m, c
}

// waitFor polls until the condition holds, so a test never depends on the exact
// moment a background poll lands.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The camera list carries what the camera says it is running, passed through
// exactly as the camera wrote it. A field a later firmware gains has to reach
// the UI without a change on this side, which is the same rule /config and
// /record already follow.
func TestCameraListCarriesTheFirmwareUnchanged(t *testing.T) {
	cam := &versionCamera{}
	const reported = `{"version":"1a6ee31","built":"Aug 31 2026 00:04:09","slot":"1/1","onTrial":false,"rolledBackFrom":"","somethingNewer":42}`
	cam.version.Store(reported)

	m, c := startCamera(t, cam)

	var view CameraView
	waitFor(t, "the camera to report its firmware", func() bool {
		views := m.Views([]store.Camera{c})
		view = views[0]
		return len(view.Firmware) > 0
	})

	var got, want map[string]any
	if err := json.Unmarshal(view.Firmware, &got); err != nil {
		t.Fatalf("firmware is not JSON: %v: %s", err, view.Firmware)
	}
	json.Unmarshal([]byte(reported), &want)
	if len(got) != len(want) {
		t.Fatalf("firmware has %d fields, the camera sent %d: %s", len(got), len(want), view.Firmware)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("firmware %s = %v, the camera said %v", k, got[k], v)
		}
	}
	// A password must never reach an API response, and the whole camera view is
	// one of those.
	if strings.Contains(string(view.Firmware), "secret") {
		t.Error("the firmware document carries the camera password")
	}
}

// The build changes about once a day and the poll runs every two seconds, so
// the answer is cached. A camera asked for its version as often as it is asked
// for its state is a camera losing frames to it.
func TestTheFirmwareIsReadFarLessOftenThanTheState(t *testing.T) {
	cam := &versionCamera{}
	cam.version.Store(`{"version":"1a6ee31"}`)

	m, c := startCamera(t, cam)
	waitFor(t, "the first firmware read", func() bool {
		return len(m.Views([]store.Camera{c})[0].Firmware) > 0
	})
	waitFor(t, "several state polls", func() bool { return cam.records.Load() >= 4 })

	if v := cam.versions.Load(); v != 1 {
		t.Errorf("/version was read %d times while /record was read %d; it should be cached",
			v, cam.records.Load())
	}
}

// A camera that will not answer for its version keeps whatever was last read.
// A firmware from before the route existed answers 404 forever, and blanking
// the field on every miss would make the UI flicker for no reason.
func TestAFirmwareThatStopsAnsweringIsNotForgotten(t *testing.T) {
	cam := &versionCamera{}
	cam.version.Store(`{"version":"1a6ee31"}`)

	m, c := startCamera(t, cam)
	waitFor(t, "the first firmware read", func() bool {
		return len(m.Views([]store.Camera{c})[0].Firmware) > 0
	})

	cam.missing.Store(true)
	before := cam.records.Load()
	waitFor(t, "more state polls", func() bool { return cam.records.Load() > before+3 })

	view := m.Views([]store.Camera{c})[0]
	if !strings.Contains(string(view.Firmware), "1a6ee31") {
		t.Errorf("firmware went missing when the route did: %s", view.Firmware)
	}
	if !view.Status.Online {
		t.Errorf("a camera that will not say its version is still online: %+v", view.Status)
	}
}

// A camera that has never answered has no firmware key at all, rather than an
// empty object that reads as a firmware reporting nothing.
func TestACameraThatHasNotAnsweredSaysNothing(t *testing.T) {
	view := CameraView{Public: store.Camera{ID: "x", Address: "10.0.0.1"}.Public()}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "firmware") {
		t.Errorf("firmware is present before the camera has answered: %s", body)
	}
}
