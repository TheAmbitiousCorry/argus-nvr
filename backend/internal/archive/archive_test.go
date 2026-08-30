package archive

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus-nvr/internal/avi"
)

// aviBytes builds a real AVI of n frames, which is what the camera's /video
// endpoint hands over and what everything here is meant to accept.
func aviBytes(t *testing.T, n int) []byte {
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
	for i := 0; i < n; i++ {
		if err := w.WriteFrame(frame, at); err != nil {
			t.Fatalf("frame: %v", err)
		}
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

func open(t *testing.T, maxBytes int64) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "recordings"), maxBytes)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	return s
}

func save(t *testing.T, s *Store, id ID, data []byte) {
	t.Helper()
	if _, err := s.Save(id, bytes.NewReader(data), Meta{Source: SourceCamera, DurMs: 10004, Frames: 213}); err != nil {
		t.Fatalf("save %s: %v", id, err)
	}
}

func TestSaveThenHeld(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	if s.Has(id) {
		t.Fatal("an empty archive claims to hold a recording")
	}
	save(t, s, id, aviBytes(t, 5))

	if !s.Has(id) {
		t.Error("a saved recording is not held")
	}
	held, err := s.Held("a1b2", "2026-08-30")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if !held["131529"] {
		t.Errorf("the day listing does not have it: %v", held)
	}
	// The identity is the path, which is what makes this survive a restart.
	if _, err := os.Stat(filepath.Join(s.Root(), "a1b2", "2026-08-30", "131529.avi")); err != nil {
		t.Errorf("not stored where its identity says: %v", err)
	}
}

// A download cut short by a camera reboot must never land under a name that
// promises a whole recording. The AVI states its own length, so the check is
// free and the half file is thrown away.
func TestTruncatedDownloadIsRefused(t *testing.T) {
	s := open(t, 0)
	whole := aviBytes(t, 20)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}

	if _, err := s.Save(id, bytes.NewReader(whole[:len(whole)/2]), Meta{Source: SourceCamera}); err == nil {
		t.Fatal("a half download was accepted")
	}
	if s.Has(id) {
		t.Error("a half download left a file where a whole one is expected")
	}
	// And nothing is left behind to pay for.
	entries, _ := os.ReadDir(filepath.Join(s.Root(), "a1b2", "2026-08-30"))
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

func TestSomethingThatIsNotAnAVIIsRefused(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	// The firmware answers a missing recording with a plain sentence, and a 200
	// carrying one of those must not become a file.
	if _, err := s.Save(id, strings.NewReader("no such recording"), Meta{}); err == nil {
		t.Fatal("a text reply was stored as a recording")
	}
	if s.Has(id) {
		t.Error("stored something that is not a recording")
	}
}

// Days and times arrive from a camera's own answers and become path segments,
// so a camera, or something answering as one, must not be able to write
// anywhere but its own corner of the volume.
func TestIdentityIsCheckedBeforeItBecomesAPath(t *testing.T) {
	s := open(t, 0)
	bad := []ID{
		{CameraID: "a1b2", Day: "../../etc", At: "131529"},
		{CameraID: "../..", Day: "2026-08-30", At: "131529"},
		{CameraID: "a1b2", Day: "2026-08-30", At: "../../passwd"},
		{CameraID: "a1b2", Day: "2026-13-45", At: "131529"},
		{CameraID: "a1b2", Day: "2026-08-30", At: "999999"},
		{CameraID: "", Day: "2026-08-30", At: "131529"},
	}
	for _, id := range bad {
		if _, err := s.Save(id, bytes.NewReader(aviBytes(t, 2)), Meta{}); err == nil {
			t.Errorf("%q was accepted as a recording identity", id)
		}
		if _, err := s.Create(id); err == nil {
			t.Errorf("%q was accepted to record into", id)
		}
		if _, _, err := s.Open(id); err != ErrNotFound {
			t.Errorf("%q was looked up rather than refused: %v", id, err)
		}
	}
}

func TestListPagesNewestFirst(t *testing.T) {
	s := open(t, 0)
	data := aviBytes(t, 3)
	for _, at := range []string{"090000", "120000", "150000"} {
		save(t, s, ID{CameraID: "a1b2", Day: "2026-08-29", At: at}, data)
		save(t, s, ID{CameraID: "c3d4", Day: "2026-08-30", At: at}, data)
	}

	recs, more, err := s.List(Filter{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !more {
		t.Error("a cut-short listing did not say there was more")
	}
	if len(recs) != 2 || recs[0].Day != "2026-08-30" || recs[0].At != "150000" {
		t.Errorf("wanted the newest two first, got %+v", recs)
	}
	if recs[0].StartedAt != "2026-08-30T15:00:00" {
		t.Errorf("start time reads %q", recs[0].StartedAt)
	}
	if recs[0].DurMs != 10004 || recs[0].Frames != 213 || recs[0].Source != SourceCamera {
		t.Errorf("the sidecar did not come back: %+v", recs[0])
	}

	rest, more, err := s.List(Filter{Start: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if more || len(rest) != 4 {
		t.Errorf("wanted the remaining four, got %d more=%v", len(rest), more)
	}

	one, _, err := s.List(Filter{CameraID: "a1b2", Day: "2026-08-29"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(one) != 3 {
		t.Errorf("wanted one camera's day, got %d", len(one))
	}

	days, err := s.Days("")
	if err != nil {
		t.Fatalf("days: %v", err)
	}
	if len(days) != 2 || days[0].Day != "2026-08-30" || days[0].Recordings != 3 {
		t.Errorf("day summary is wrong: %+v", days)
	}
}

func TestRetentionTakesTheOldestFirst(t *testing.T) {
	data := aviBytes(t, 40)
	s := open(t, int64(len(data))*2+512) // room for two, near enough

	for _, at := range []string{"090000", "120000", "150000", "180000"} {
		save(t, s, ID{CameraID: "a1b2", Day: "2026-08-30", At: at}, data)
	}
	removed, freed, err := s.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed == 0 || freed == 0 {
		t.Fatalf("nothing was aged out of an archive over its limit")
	}

	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("retention emptied the archive")
	}
	if recs[0].At != "180000" {
		t.Errorf("the newest recording was not kept: %+v", recs)
	}
	for _, r := range recs {
		if r.At == "090000" {
			t.Error("the oldest recording survived while newer ones went")
		}
	}
	usage, err := s.Usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Bytes > usage.MaxBytes {
		t.Errorf("still %d bytes over a %d byte limit", usage.Bytes, usage.MaxBytes)
	}
}

// The rule that matters most: whatever else retention does, it must not delete
// a recording that is still being written.
func TestRetentionLeavesAWriteInProgressAlone(t *testing.T) {
	s := open(t, 1) // over its limit the moment anything exists

	pending, err := s.Create(ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	frame := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x01, 0xE0, 0x02, 0x80, 0x03,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xD9}
	at := time.Unix(1700000000, 0)
	for i := 0; i < 30; i++ {
		if err := pending.WriteFrame(frame, at); err != nil {
			t.Fatalf("frame: %v", err)
		}
		at = at.Add(40 * time.Millisecond)
	}
	// Two finished recordings beside it, so retention has something it is
	// allowed to take and a reason to be looking.
	save(t, s, ID{CameraID: "a1b2", Day: "2026-08-29", At: "090000"}, aviBytes(t, 5))
	save(t, s, ID{CameraID: "a1b2", Day: "2026-08-29", At: "100000"}, aviBytes(t, 5))

	removed, _, err := s.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d recordings, wanted the one it was allowed to take", removed)
	}

	rec, err := pending.Commit()
	if err != nil {
		t.Fatalf("the recording being written did not survive the sweep: %v", err)
	}
	if rec.Frames != 30 || rec.Source != SourceService {
		t.Errorf("committed %+v", rec)
	}
	if !s.Has(rec.ID()) {
		t.Error("the committed recording is not held")
	}
	usage, err := s.Usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Recordings != 2 {
		t.Errorf("expected the newest finished recording and the committed one, found %d", usage.Recordings)
	}
}

// A recording that never finished is a header of zeroes under a hidden name.
// It must not be listed, and it must not be there after a restart.
func TestUnfinishedRecordingsAreSweptAtStartup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recordings")
	s, err := Open(root, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pending, err := s.Create(ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := pending.WriteFrame([]byte{0xFF, 0xD8, 0xFF, 0xD9}, time.Now()); err != nil {
		t.Fatalf("frame: %v", err)
	}
	// No commit: this is what a power cut leaves.

	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("an unfinished recording was listed: %+v", recs)
	}

	if _, err := Open(root, 0); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "a1b2", "2026-08-30"))
	for _, e := range entries {
		t.Errorf("survived a restart: %s", e.Name())
	}
}

// A recording too short to be worth keeping leaves nothing at all.
func TestAbortLeavesNothing(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	pending, err := s.Create(id)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pending.WriteFrame([]byte{0xFF, 0xD8, 0xFF, 0xD9}, time.Now())
	pending.Abort()

	if s.Has(id) {
		t.Error("an aborted recording is held")
	}
	entries, _ := os.ReadDir(filepath.Join(s.Root(), "a1b2", "2026-08-30"))
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

// A recording written on a camera's behalf must be as playable as one pulled
// off a card, which starts with saying its own length correctly.
func TestRecordedFileIsWellFormed(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	pending, err := s.Create(id)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	frame := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x01, 0xE0, 0x02, 0x80, 0x03,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xD9}
	at := time.Unix(1700000000, 0)
	for i := 0; i < 10; i++ {
		pending.WriteFrame(frame, at)
		at = at.Add(50 * time.Millisecond)
	}
	if _, err := pending.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "AVI " {
		t.Fatalf("not an AVI: % x", data[0:12])
	}
	if got, want := int(binary.LittleEndian.Uint32(data[4:8]))+8, len(data); got != want {
		t.Errorf("says %d bytes, is %d", got, want)
	}
}

// Retention and pulling have to agree, or they fight: retention ages a
// recording out to stay under the limit, the camera still has it on its card,
// and the puller downloads it again on the next pass, forever.
func TestAnAgedOutRecordingIsNotWantedBack(t *testing.T) {
	data := aviBytes(t, 40)
	s := open(t, int64(len(data))*2+512)

	for _, at := range []string{"090000", "120000", "150000", "180000"} {
		save(t, s, ID{CameraID: "a1b2", Day: "2026-08-30", At: at}, data)
	}
	if _, _, err := s.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	held, err := s.Held("a1b2", "2026-08-30")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	// Every recording the day ever had is settled: held, or let go on purpose.
	for _, at := range []string{"090000", "120000", "150000", "180000"} {
		if !held[at] {
			t.Errorf("%s would be pulled again after being aged out", at)
		}
	}
	// And what was aged out is not offered as something to play.
	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) >= 4 {
		t.Errorf("aged-out recordings are still listed: %+v", recs)
	}
}
