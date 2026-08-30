package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/avi"
	"argus-nvr/internal/camera"
	"argus-nvr/internal/store"
)

// fakeCard is a camera with a card, answering the two listing routes the design
// document adds and the /video route the firmware already has. It exists
// because the firmware half of this is being written at the same time as this
// half, so the only thing to build against is the document.
type fakeCard struct {
	mu sync.Mutex
	// days maps a day to the recordings in it, in the order the camera would
	// list them.
	days map[string][]camera.Recording
	// perPage cuts a day's answer short, which is what `more` is for.
	perPage int
	// truncate serves a download cut off part way, the way a camera that
	// rebooted mid-transfer does.
	truncate bool
	// noListing is a camera running firmware from before the listing existed.
	noListing bool

	downloads   int32
	inFlight    int32
	maxInFlight int32
	listings    int32
}

func (f *fakeCard) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/recordings/days", func(w http.ResponseWriter, r *http.Request) {
		if f.noListing {
			http.Error(w, "no such route", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		days := make([]string, 0, len(f.days))
		for day := range f.days {
			days = append(days, day)
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"days": days})
	})
	mux.HandleFunc("/recordings", func(w http.ResponseWriter, r *http.Request) {
		if f.noListing {
			http.Error(w, "no such route", http.StatusNotFound)
			return
		}
		atomic.AddInt32(&f.listings, 1)
		day := r.URL.Query().Get("day")
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))

		f.mu.Lock()
		recs := append([]camera.Recording(nil), f.days[day]...)
		perPage := f.perPage
		f.mu.Unlock()

		if start > len(recs) {
			start = len(recs)
		}
		recs = recs[start:]
		more := false
		if perPage > 0 && len(recs) > perPage {
			recs, more = recs[:perPage], true
		}
		json.NewEncoder(w).Encode(map[string]any{"day": day, "recordings": recs, "more": more})
	})
	mux.HandleFunc("/video", func(w http.ResponseWriter, r *http.Request) {
		now := atomic.AddInt32(&f.inFlight, 1)
		for {
			max := atomic.LoadInt32(&f.maxInFlight)
			if now <= max || atomic.CompareAndSwapInt32(&f.maxInFlight, max, now) {
				break
			}
		}
		defer atomic.AddInt32(&f.inFlight, -1)
		atomic.AddInt32(&f.downloads, 1)

		dir := r.URL.Query().Get("dir")
		parts := strings.Split(strings.TrimPrefix(dir, "/rec/"), "/")
		if len(parts) != 2 {
			http.Error(w, "no such recording", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		var found bool
		for _, rec := range f.days[parts[0]] {
			if rec.At == parts[1] {
				found = true
			}
		}
		truncate := f.truncate
		f.mu.Unlock()
		if !found {
			http.Error(w, "no such recording", http.StatusNotFound)
			return
		}

		body := aviOf(t, 12)
		if truncate {
			body = body[:len(body)/2]
		}
		w.Header().Set("Content-Type", "video/x-msvideo")
		// The radio is shared with the live view, so a download is not
		// instant, and this is where "one at a time" is worth proving.
		time.Sleep(20 * time.Millisecond)
		w.Write(body)
	})
	return mux
}

// aviOf builds a real AVI, so the download path is exercised end to end rather
// than against a placeholder that would pass any check.
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

// deviceFor builds one device by hand, which is how a test drives a single pass
// of the puller without waiting out the intervals the real thing runs on.
func deviceFor(t *testing.T, card *fakeCard) (*device, *archive.Store, *fakeCard) {
	t.Helper()
	srv := httptest.NewServer(card.handler(t))
	t.Cleanup(srv.Close)

	arch, err := archive.Open(filepath.Join(t.TempDir(), "recordings"), 0)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	cam := store.Camera{
		ID:      "cam1",
		Address: strings.TrimPrefix(srv.URL, "http://"),
		Name:    "alpha",
		User:    "admin",
		Pass:    "secret",
	}
	client := camera.New(cam)
	hub := camera.NewHub(client)
	t.Cleanup(hub.Close)
	d := &device{cam: cam, client: client, hub: hub, frames: hub, arch: arch}
	d.status.Online = true

	// The pause between downloads is there for a shared radio, and there is no
	// radio here.
	was := betweenDownloads
	betweenDownloads = time.Millisecond
	t.Cleanup(func() { betweenDownloads = was })

	return d, arch, card
}

func recordingsOf(n int) []camera.Recording {
	out := make([]camera.Recording, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, camera.Recording{
			At:     fmt.Sprintf("1%05d", i),
			DurMs:  10004,
			Bytes:  12256740,
			Frames: 213,
		})
	}
	return out
}

// The whole point: a camera that was cut off keeps recording to its card, and
// the service finds what it has not seen and pulls it.
func TestPullTakesWhatItDoesNotHave(t *testing.T) {
	card := &fakeCard{days: map[string][]camera.Recording{
		"2026-08-29": recordingsOf(3),
		"2026-08-30": recordingsOf(2),
	}}
	d, arch, _ := deviceFor(t, card)

	if !d.pullOnce(context.Background()) {
		t.Fatal("a camera with the listing was treated as not having it")
	}
	recs, _, err := arch.List(archive.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 5 {
		t.Fatalf("held %d recordings, wanted 5: %+v", len(recs), recs)
	}
	if recs[0].Source != archive.SourceCamera || recs[0].DurMs != 10004 || recs[0].Frames != 213 {
		t.Errorf("what the camera said about the recording was not kept: %+v", recs[0])
	}

	// Downloading is idempotent: a second pass over the same camera costs it
	// listings and nothing else. This is what stops a catch-up loop from
	// re-fetching the whole card every minute.
	before := atomic.LoadInt32(&card.downloads)
	d.pullOnce(context.Background())
	if after := atomic.LoadInt32(&card.downloads); after != before {
		t.Errorf("a second pass downloaded %d more recordings", after-before)
	}
}

// Sequential per camera, so a catch-up after an outage does not saturate the
// radio the live view is sharing.
func TestPullDownloadsOneAtATime(t *testing.T) {
	card := &fakeCard{days: map[string][]camera.Recording{"2026-08-30": recordingsOf(6)}}
	d, _, _ := deviceFor(t, card)

	d.pullOnce(context.Background())
	if got := atomic.LoadInt32(&card.maxInFlight); got != 1 {
		t.Errorf("%d downloads were open at once, wanted 1", got)
	}
}

// A day too big for one answer is paged, using the same `more` flag and `start`
// offset the firmware's own file listing already uses.
func TestPullPagesADayThatSaysThereIsMore(t *testing.T) {
	card := &fakeCard{
		days:    map[string][]camera.Recording{"2026-08-30": recordingsOf(7)},
		perPage: 2,
	}
	d, arch, _ := deviceFor(t, card)

	d.pullOnce(context.Background())
	recs, _, err := arch.List(archive.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 7 {
		t.Errorf("held %d of 7 recordings across the pages", len(recs))
	}
	if atomic.LoadInt32(&card.listings) < 4 {
		t.Errorf("the day was not paged: %d listing requests", card.listings)
	}
}

// An interrupted download leaves no half file where a whole one is expected,
// and the next pass picks it up.
func TestPullRetriesAnInterruptedDownload(t *testing.T) {
	card := &fakeCard{
		days:     map[string][]camera.Recording{"2026-08-30": recordingsOf(1)},
		truncate: true,
	}
	d, arch, _ := deviceFor(t, card)

	d.pullOnce(context.Background())
	if recs, _, _ := arch.List(archive.Filter{}); len(recs) != 0 {
		t.Fatalf("a cut-off download was kept: %+v", recs)
	}

	card.mu.Lock()
	card.truncate = false
	card.mu.Unlock()

	d.pullOnce(context.Background())
	recs, _, err := arch.List(archive.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("the retry did not land: %+v", recs)
	}
	data, err := os.ReadFile(arch.Path(recs[0].ID()))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("RIFF")) {
		t.Error("what was stored is not an AVI")
	}
}

// A camera running firmware from before the listing existed is a normal state,
// not a fault. It is said once and then asked again more slowly.
func TestFirmwareWithoutTheListingIsNotAFault(t *testing.T) {
	card := &fakeCard{noListing: true, days: map[string][]camera.Recording{}}
	d, arch, _ := deviceFor(t, card)

	if d.pullOnce(context.Background()) {
		t.Error("a camera with no listing was treated as having one")
	}
	if recs, _, _ := arch.List(archive.Filter{}); len(recs) != 0 {
		t.Errorf("something was stored for a camera that lists nothing: %+v", recs)
	}
	if d.snapshotStatus().PullError == "" {
		t.Error("the status says nothing about why nothing is being pulled")
	}
}

// A camera that is not answering its poll is left alone rather than asked
// anyway. Its recordings are on its card and they will still be there.
func TestAnOfflineCameraIsNotAsked(t *testing.T) {
	card := &fakeCard{days: map[string][]camera.Recording{"2026-08-30": recordingsOf(2)}}
	d, _, _ := deviceFor(t, card)
	d.status.Online = false

	d.pullOnce(context.Background())
	if got := atomic.LoadInt32(&card.listings); got != 0 {
		t.Errorf("an offline camera was asked for %d listings", got)
	}
}

// fakeFrames stands in for the stream fan-out, so a recording made on a
// camera's behalf can be driven frame by frame.
type fakeFrames struct {
	ch chan []byte
}

func (f *fakeFrames) Subscribe() (<-chan []byte, func()) { return f.ch, func() {} }

// A camera with no card cannot record, so the service records for it, out of
// the frames it is already receiving.
func TestRecordsForACameraThatCannot(t *testing.T) {
	d, arch, _ := deviceFor(t, &fakeCard{days: map[string][]camera.Recording{}})

	frames := make(chan []byte, 64)
	d.frames = &fakeFrames{ch: frames}
	d.storage = "missing"
	d.recording = true

	frame := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x01, 0xE0, 0x02, 0x80, 0x03,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xD9}
	for i := 0; i < 20; i++ {
		frames <- frame
	}
	// Closing is the hub saying it has no more frames, which ends the
	// recording with what did arrive rather than losing it.
	close(frames)

	d.standInOnce(context.Background())

	recs, _, err := arch.List(archive.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recorded %d clips for a camera that cannot record, wanted 1", len(recs))
	}
	if recs[0].Source != archive.SourceService {
		t.Errorf("recorded as %q, wanted %q", recs[0].Source, archive.SourceService)
	}
	if recs[0].Frames != 20 {
		t.Errorf("kept %d of 20 frames", recs[0].Frames)
	}
	if recs[0].CameraID != "cam1" {
		t.Errorf("stored under camera %q", recs[0].CameraID)
	}
	data, err := os.ReadFile(arch.Path(recs[0].ID()))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("RIFF")) {
		t.Error("what was recorded is not an AVI")
	}
	if d.snapshotStatus().StandIn {
		t.Error("still reporting itself as recording after the recording ended")
	}
}

// The rule is the document's: no usable card, and the camera says it is
// recording. Anything else and the service keeps out of it, in particular a
// camera whose firmware says nothing about storage at all.
func TestWhenToRecordForACamera(t *testing.T) {
	cases := []struct {
		storage   string
		recording bool
		online    bool
		want      bool
	}{
		{"missing", true, true, true},
		{"unwritable", true, true, true},
		{"ok", true, true, false},
		{"missing", false, true, false},
		{"missing", true, false, false},
		{"", true, true, false},
	}
	for _, c := range cases {
		d := &device{}
		d.storage, d.recording, d.status.Online = c.storage, c.recording, c.online
		if got := d.shouldStandIn(); got != c.want {
			t.Errorf("storage=%q recording=%v online=%v: %v, wanted %v",
				c.storage, c.recording, c.online, got, c.want)
		}
	}
}

// A recording of a frame or two has nothing to see and no measurable rate.
func TestAVeryShortRecordingIsNotKept(t *testing.T) {
	d, arch, _ := deviceFor(t, &fakeCard{days: map[string][]camera.Recording{}})

	frames := make(chan []byte, 4)
	d.frames = &fakeFrames{ch: frames}
	d.storage = "missing"
	d.recording = true
	frames <- []byte{0xFF, 0xD8, 0xFF, 0xD9}
	close(frames)

	d.standInOnce(context.Background())

	if recs, _, _ := arch.List(archive.Filter{}); len(recs) != 0 {
		t.Errorf("kept a recording of one frame: %+v", recs)
	}
	usage, err := arch.Usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Bytes != 0 {
		t.Errorf("%d bytes left behind by a recording that was thrown away", usage.Bytes)
	}
}
