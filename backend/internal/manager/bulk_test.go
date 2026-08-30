package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"argus-nvr/internal/camera"
	"argus-nvr/internal/store"
)

// fleet is a set of fake cameras sharing one log of what happened to them, so a
// test can assert on the order events happened in across devices as well as on
// what each device saw.
type fleet struct {
	mu     sync.Mutex
	events []string
	// flashing counts uploads in progress. Cameras reboot as they finish, so
	// two at once is the state this must never reach.
	flashing    int
	maxFlashing int
}

func (f *fleet) log(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

type fakeDevice struct {
	fleet *fleet
	name  string
	// failConfig stands in for a camera that is reachable but will not answer
	// with its settings, and failFlash for one that refuses the image.
	failConfig bool
	failFlash  bool
	uploads    int
	mu         sync.Mutex
}

func (d *fakeDevice) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/record", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"active":false}`)
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if d.failConfig {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{"bri":0,"con":0,"autoimg":true,"moten":true,"motsens":15,"schdays":1,"schen":true}`)
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		d.fleet.log("image:" + d.name)
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("/recording", func(w http.ResponseWriter, r *http.Request) {
		d.fleet.log("recording:" + d.name)
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		d.fleet.mu.Lock()
		d.fleet.flashing++
		if d.fleet.flashing > d.fleet.maxFlashing {
			d.fleet.maxFlashing = d.fleet.flashing
		}
		d.fleet.mu.Unlock()
		defer func() {
			d.fleet.mu.Lock()
			d.fleet.flashing--
			d.fleet.mu.Unlock()
		}()

		n, _ := io.Copy(io.Discard, r.Body)
		// A real flash takes seconds; this is only long enough that two
		// running at once would overlap.
		time.Sleep(20 * time.Millisecond)

		d.mu.Lock()
		d.uploads++
		d.mu.Unlock()
		d.fleet.log(fmt.Sprintf("update:%s:%d", d.name, n))

		if d.failFlash {
			http.Error(w, "flash write failed", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, "ok")
	})
	return mux
}

func (d *fakeDevice) uploadCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.uploads
}

// startFleet builds a manager holding one device per fake, and returns the
// camera ids in the same order.
func startFleet(t *testing.T, devices ...*fakeDevice) (*Manager, []string) {
	t.Helper()

	f := &fleet{}
	m := New(nil, nil)
	t.Cleanup(m.Close)

	cams := make([]store.Camera, 0, len(devices))
	ids := make([]string, 0, len(devices))
	for i, d := range devices {
		d.fleet = f
		if d.name == "" {
			d.name = fmt.Sprintf("cam%d", i)
		}
		srv := httptest.NewServer(d.handler())
		t.Cleanup(srv.Close)

		cams = append(cams, store.Camera{
			ID:      d.name,
			Address: strings.TrimPrefix(srv.URL, "http://"),
			Name:    d.name,
			User:    "admin",
			Pass:    "secret",
		})
		ids = append(ids, d.name)
	}
	m.Sync(cams)
	return m, ids
}

func patch(t *testing.T, doc string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Each camera reboots as it finishes, so flashing two at once is how a fleet
// ends up in a state nobody can describe.
func TestFlashIsSequential(t *testing.T) {
	devices := []*fakeDevice{{name: "a"}, {name: "b"}, {name: "c"}}
	m, ids := startFleet(t, devices...)

	results := m.Flash(context.Background(), ids, make([]byte, 4096))

	if len(results) != 3 {
		t.Fatalf("expected a result per camera, got %d", len(results))
	}
	for i, r := range results {
		if !r.OK || r.Error != "" {
			t.Errorf("%s: %+v", ids[i], r)
		}
		if r.CameraID != ids[i] {
			t.Errorf("result %d is for %s, want %s", i, r.CameraID, ids[i])
		}
		if r.Bytes != 4096 {
			t.Errorf("%s: reported %d bytes, want 4096", r.CameraID, r.Bytes)
		}
	}

	f := devices[0].fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxFlashing != 1 {
		t.Errorf("%d cameras were flashed at once, want 1", f.maxFlashing)
	}
	want := []string{"update:a:4096", "update:b:4096", "update:c:4096"}
	if strings.Join(f.events, ",") != strings.Join(want, ",") {
		t.Errorf("flash order\n got %v\nwant %v", f.events, want)
	}
}

// A failure part way through must leave the cameras behind it untouched and
// still reported, rather than carrying on into a fleet nobody can account for.
func TestFlashStopsAfterAFailure(t *testing.T) {
	devices := []*fakeDevice{{name: "a"}, {name: "b", failFlash: true}, {name: "c"}}
	m, ids := startFleet(t, devices...)

	results := m.Flash(context.Background(), ids, make([]byte, 1024))

	if len(results) != 3 {
		t.Fatalf("expected a result per camera, got %d", len(results))
	}
	if !results[0].OK {
		t.Errorf("first camera should have been flashed: %+v", results[0])
	}
	if results[1].OK || results[1].Error == "" {
		t.Errorf("second camera should have reported its failure: %+v", results[1])
	}
	if results[2].OK || results[2].Error == "" {
		t.Errorf("third camera should have been reported as untouched: %+v", results[2])
	}
	if n := devices[2].uploadCount(); n != 0 {
		t.Errorf("third camera was flashed %d times after an earlier failure", n)
	}
}

func TestFlashUnknownCameraDoesNotStopTheRest(t *testing.T) {
	devices := []*fakeDevice{{name: "a"}, {name: "b"}}
	m, ids := startFleet(t, devices...)

	results := m.Flash(context.Background(), []string{ids[0], "gone", ids[1]}, make([]byte, 128))

	if len(results) != 3 {
		t.Fatalf("expected three results, got %d", len(results))
	}
	if results[1].OK || results[1].Error != "no such camera" {
		t.Errorf("unknown camera: %+v", results[1])
	}
	if !results[0].OK || !results[2].OK {
		t.Errorf("real cameras should still have been flashed: %+v", results)
	}
}

// One camera being unreachable is news about that camera. The rest of the fleet
// still gets the settings the operator asked for.
func TestApplySettingsReportsEachCameraSeparately(t *testing.T) {
	devices := []*fakeDevice{{name: "a"}, {name: "b", failConfig: true}, {name: "c"}}
	m, ids := startFleet(t, devices...)

	results := m.ApplySettings(context.Background(), []string{ids[0], ids[1], ids[2], "gone"}, camera.Settings{
		Image:     patch(t, `{"bri":1}`),
		Recording: patch(t, `{"motsens":20}`),
	})

	if len(results) != 4 {
		t.Fatalf("expected four results, got %d", len(results))
	}
	for _, i := range []int{0, 2} {
		if !results[i].OK || results[i].Error != "" {
			t.Errorf("%s should have succeeded: %+v", results[i].CameraID, results[i])
		}
	}
	if results[1].OK || results[1].Error == "" {
		t.Errorf("the unreachable camera should have reported an error: %+v", results[1])
	}
	if results[3].OK || results[3].Error != "no such camera" {
		t.Errorf("unknown camera: %+v", results[3])
	}
	for i, want := range []string{ids[0], ids[1], ids[2], "gone"} {
		if results[i].CameraID != want {
			t.Errorf("result %d is for %s, want %s", i, results[i].CameraID, want)
		}
	}

	// Both forms were posted to each camera that answered, and the camera that
	// did not answer was never posted to at all.
	f := devices[0].fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	got := strings.Join(f.events, ",")
	for _, want := range []string{"image:a", "recording:a", "image:c", "recording:c"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %v", want, f.events)
		}
	}
	if strings.Contains(got, ":b") {
		t.Errorf("the unreachable camera was posted to anyway: %v", f.events)
	}
}

func TestConfigForUnknownCamera(t *testing.T) {
	m, _ := startFleet(t, &fakeDevice{name: "a"})
	if _, err := m.Config(context.Background(), "gone"); err == nil {
		t.Fatal("expected an error for a camera that is not configured")
	}
}
