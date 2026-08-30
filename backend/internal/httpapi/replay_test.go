package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"argus-nvr/internal/archive"
)

// replayed reads a multipart/x-mixed-replace body back into the frames it
// carried, which is what a viewer's img element does with it.
func replayed(t *testing.T, contentType string, body []byte) [][]byte {
	t.Helper()
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("content type %q: %v", contentType, err)
	}
	if kind != "multipart/x-mixed-replace" {
		t.Fatalf("content type is %q, want multipart/x-mixed-replace", kind)
	}
	boundary, ok := params["boundary"]
	if !ok {
		t.Fatal("no boundary in the content type")
	}

	var out [][]byte
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("part %d: %v", len(out), err)
		}
		if got := part.Header.Get("Content-Type"); got != "image/jpeg" {
			t.Errorf("part %d is %q, want image/jpeg", len(out), got)
		}
		frame, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part %d: %v", len(out), err)
		}
		out = append(out, frame)
	}
}

// framesOf pulls the individual JPEGs back out of the AVI the fixture wrote, so
// a replay can be checked against the bytes that went in rather than against
// itself.
func framesOf(t *testing.T, clip []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for at := 224; at+8 <= len(clip); {
		if string(clip[at:at+4]) == "idx1" {
			break
		}
		n := int(clip[at+4]) | int(clip[at+5])<<8 | int(clip[at+6])<<16 | int(clip[at+7])<<24
		if string(clip[at+2:at+4]) != "dc" || at+8+n > len(clip) {
			break
		}
		out = append(out, clip[at+8:at+8+n])
		at += 8 + n + n%2
	}
	return out
}

// The frame index is what lets a scrubber land somewhere without downloading
// twelve megabytes first.
func TestFrameIndex(t *testing.T) {
	h, _, _ := serverWithRecordings(t)

	w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/frames")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var doc struct {
		Frames int     `json:"frames"`
		DurMs  int64   `json:"durMs"`
		Width  int     `json:"width"`
		Height int     `json:"height"`
		Times  []int64 `json:"times"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body)
	}
	if doc.Frames != 10 || len(doc.Times) != 10 {
		t.Fatalf("frames=%d times=%d, want ten of each", doc.Frames, len(doc.Times))
	}
	// The fixture's frames say 640x480 in their start-of-frame marker. A
	// recording made before a resolution change still has to describe itself.
	if doc.Width != 640 || doc.Height != 480 {
		t.Errorf("size is %dx%d, want 640x480 as the frames say", doc.Width, doc.Height)
	}
	// The length the camera reported is what the recording is listed under, so
	// it is what the scrubber is measured against too.
	if doc.DurMs != 10004 {
		t.Errorf("durMs = %d, want the 10004 the sidecar holds", doc.DurMs)
	}
	for i, at := range doc.Times {
		if want := int64(i) * 40; at != want {
			t.Errorf("frame %d is at %dms, want %dms", i, at, want)
		}
	}
}

func TestFrameIndexOfSomethingNotHeld(t *testing.T) {
	h, _, _ := serverWithRecordings(t)
	for _, path := range []string{
		"/api/recordings/a1b2/2026-08-30/000000/frames",
		"/api/recordings/a1b2/not-a-day/131529/frames",
		"/api/recordings/a1b2!/2026-08-30/131529/frames",
		"/api/recordings/a1b2/2026-08-30/2515/frames",
	} {
		if w := get(t, h, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s gave %d, want 404", path, w.Code)
		}
	}
}

// The replay has to hand over exactly the frames that were written, in order.
// Anything else is a viewer being shown something that is not the recording.
func TestReplaySendsEveryFrame(t *testing.T) {
	h, _, clip := serverWithRecordings(t)
	want := framesOf(t, clip)
	if len(want) != 10 {
		t.Fatalf("the fixture holds %d frames, expected ten", len(want))
	}

	w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream?speed=0")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := replayed(t, w.Header().Get("Content-Type"), w.Body.Bytes())
	if len(got) != len(want) {
		t.Fatalf("replayed %d frames, the recording holds %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("replayed frame %d is not the frame that was recorded", i)
		}
	}
}

// Seeking is the whole reason from exists: a scrubber dropped in the middle of
// a recording must not replay the part before it.
func TestReplayFromSkipsWhatCameBefore(t *testing.T) {
	h, _, clip := serverWithRecordings(t)
	want := framesOf(t, clip)

	w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream?from=6&speed=0")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := replayed(t, w.Header().Get("Content-Type"), w.Body.Bytes())
	if len(got) != len(want)-6 {
		t.Fatalf("replayed %d frames from frame six of %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[6+i]) {
			t.Errorf("frame %d of the seek is not frame %d of the recording", i, 6+i)
		}
	}

	// A scrubber that has run to the end asks for the frame after the last one.
	// That is the end of the recording rather than a mistake.
	w = get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream?from=99&speed=0")
	if got := replayed(t, w.Header().Get("Content-Type"), w.Body.Bytes()); len(got) != 1 {
		t.Errorf("seeking past the end replayed %d frames, want the last one", len(got))
	}
}

// Speed is what makes this a replay rather than a download. The fixture is ten
// frames forty milliseconds apart, so at full speed it takes the length of the
// recording and at four times it takes a quarter of that.
func TestReplayIsPacedFromTheFile(t *testing.T) {
	h, _, _ := serverWithRecordings(t)
	const span = 9 * 40 * time.Millisecond // first frame to last

	tests := []struct {
		speed string
		least time.Duration
		most  time.Duration
	}{
		{"1", span - 20*time.Millisecond, span + 400*time.Millisecond},
		{"4", span/4 - 20*time.Millisecond, span/4 + 400*time.Millisecond},
		{"0", 0, 200 * time.Millisecond},
	}
	for _, tt := range tests {
		start := time.Now()
		w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream?speed="+tt.speed)
		took := time.Since(start)
		if w.Code != http.StatusOK {
			t.Fatalf("speed %s: status %d", tt.speed, w.Code)
		}
		if n := len(replayed(t, w.Header().Get("Content-Type"), w.Body.Bytes())); n != 10 {
			t.Errorf("speed %s replayed %d frames, want ten", tt.speed, n)
		}
		if took < tt.least || took > tt.most {
			t.Errorf("speed %s took %v, want between %v and %v", tt.speed, took, tt.least, tt.most)
		}
	}
}

func TestReplayRefusesASpeedItCannotHold(t *testing.T) {
	h, _, _ := serverWithRecordings(t)
	for _, speed := range []string{"-1", "100", "quickly"} {
		w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream?speed="+speed)
		if w.Code != http.StatusBadRequest {
			t.Errorf("speed=%s gave %d, want 400", speed, w.Code)
		}
	}
}

// A recording that will not index is news about that recording. It must not
// take the service down or answer with a body that is not a replay.
func TestReplayingSomethingThatIsNotARecording(t *testing.T) {
	h, arch, _ := serverWithRecordings(t)
	// Bypass Save, which checks the file is an AVI, to leave the kind of file a
	// half-finished write from an older version might have left.
	path := arch.File(recordingIDFor("a1b2", "2026-08-30", "131529"), archive.FormatAVI)
	if err := writeFile(path, []byte("RIFF\x04\x00\x00\x00AVI ")); err != nil {
		t.Fatalf("write: %v", err)
	}
	w := get(t, h, "/api/recordings/a1b2/2026-08-30/131529/stream")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q, want JSON", ct)
	}
}

func recordingIDFor(cameraID, day, at string) archive.ID {
	return archive.ID{CameraID: cameraID, Day: day, At: at}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
