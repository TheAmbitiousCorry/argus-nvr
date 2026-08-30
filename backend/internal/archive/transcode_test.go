package archive

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeEncoder stands in for ffmpeg so the archive's rules can be tested without
// one on the machine, and so the encodes that must be rejected can be produced
// on purpose: an encoder that quietly drops frames, or one that writes a file
// that cannot be read back.
type fakeEncoder struct {
	// frames is what a decode of the output will report. Zero means: report
	// however many the encoder was told to write.
	frames int
	// short writes an output holding fewer frames than the recording did, which
	// is the failure that must never cost the AVI.
	short int
	// truncate writes an output that cannot be decoded at all.
	truncate bool
	// failEncode is ffmpeg refusing the input.
	failEncode bool

	encodes int
	wrote   int
}

func (f *fakeEncoder) Encode(ctx context.Context, src, dst string) error {
	f.encodes++
	if f.failEncode {
		return errors.New("ffmpeg: no such encoder")
	}
	// The output stands in for an MP4: a line per frame, so counting it back is
	// counting frames.
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	n := len(in) / 100
	if n < 1 {
		n = 1
	}
	if f.short > 0 {
		n = f.short
	}
	f.wrote = n
	body := strings.Repeat("frame\n", n)
	if f.truncate {
		body = "this is not a recording"
	}
	return os.WriteFile(dst, []byte(body), 0o600)
}

func (f *fakeEncoder) Frames(ctx context.Context, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(string(data), "frame\n") {
		return 0, errors.New("ffmpeg: the decode did not finish")
	}
	if f.frames > 0 {
		return f.frames, nil
	}
	return strings.Count(string(data), "frame\n"), nil
}

// matching is an encoder that always produces exactly the frames the AVI holds,
// which is the only case where the AVI may be deleted.
func matching(t *testing.T, s *Store, id ID) *fakeEncoder {
	t.Helper()
	want, err := aviFrames(s.File(id, FormatAVI))
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	return &fakeEncoder{frames: want}
}

func TestTranscodeReplacesTheAVIOnlyWhenTheFramesMatch(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))
	before, err := os.Stat(s.File(id, FormatAVI))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	out, err := s.Transcode(context.Background(), id, matching(t, s, id))
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	if out.Frames != 12 {
		t.Errorf("verified %d frames, the recording holds 12", out.Frames)
	}
	if out.Before != before.Size() {
		t.Errorf("before is %d, the AVI was %d", out.Before, before.Size())
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); !os.IsNotExist(err) {
		t.Error("the AVI is still there after a verified transcode")
	}
	if _, err := os.Stat(s.File(id, FormatMP4)); err != nil {
		t.Errorf("no MP4 after a verified transcode: %v", err)
	}

	// The identity is unchanged: same camera, same day, same start time.
	if !s.Has(id) {
		t.Error("a transcoded recording is not held any more")
	}
	f, _, format, err := s.Open(id)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Close()
	if format != FormatMP4 {
		t.Errorf("opened as %q, wanted mp4", format)
	}

	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].Format != FormatMP4 || recs[0].At != "131529" {
		t.Fatalf("listing after a transcode: %+v", recs)
	}
	// The sidecar is untouched, so what the camera said about the recording
	// survives the re-encoding of it.
	if recs[0].DurMs != 10004 || recs[0].Source != SourceCamera {
		t.Errorf("the sidecar did not survive: %+v", recs[0])
	}
}

func TestTranscodeKeepsTheAVIWhenTheEncodeIsShort(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))

	// An encode that silently dropped half the recording.
	_, err := s.Transcode(context.Background(), id, &fakeEncoder{short: 6})
	if err == nil {
		t.Fatal("a transcode holding half the frames was accepted")
	}
	if !strings.Contains(err.Error(), "AVI is kept") {
		t.Errorf("unhelpful error: %v", err)
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); err != nil {
		t.Fatalf("the AVI was deleted for a short encode: %v", err)
	}
	if _, err := os.Stat(s.File(id, FormatMP4)); !os.IsNotExist(err) {
		t.Error("a rejected encode was left in place as an MP4")
	}
	// Nothing was left behind under a temp name either.
	assertNoTemps(t, s, id)
}

func TestTranscodeKeepsTheAVIWhenTheEncodeCannotBeRead(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))

	if _, err := s.Transcode(context.Background(), id, &fakeEncoder{truncate: true}); err == nil {
		t.Fatal("a truncated encode was accepted")
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); err != nil {
		t.Fatalf("the AVI was deleted for a truncated encode: %v", err)
	}
	assertNoTemps(t, s, id)
}

func TestTranscodeKeepsTheAVIWhenFFmpegFails(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))

	if _, err := s.Transcode(context.Background(), id, &fakeEncoder{failEncode: true}); err == nil {
		t.Fatal("a failed encode was reported as a success")
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); err != nil {
		t.Fatalf("the AVI was deleted after ffmpeg failed: %v", err)
	}
	assertNoTemps(t, s, id)
}

func TestTranscodeRefusesARecordingItCannotIndex(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))
	// A recording cut down to less than its own header is one nothing can
	// count the frames of, so there is nothing to verify a replacement against.
	if err := os.Truncate(s.File(id, FormatAVI), 50); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	enc := &fakeEncoder{}
	if _, err := s.Transcode(context.Background(), id, enc); err == nil {
		t.Fatal("a recording that cannot be indexed was transcoded anyway")
	}
	if enc.encodes != 0 {
		t.Error("ffmpeg was run on a recording that could not be counted first")
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); err != nil {
		t.Fatalf("the AVI went missing: %v", err)
	}
}

func TestTranscodeFinishesAnInterruptedOne(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))
	// What a crash between renaming the MP4 in and removing the AVI leaves.
	if err := os.WriteFile(s.File(id, FormatMP4), []byte("frame\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	enc := &fakeEncoder{}
	_, err := s.Transcode(context.Background(), id, enc)
	if !errors.Is(err, ErrAlreadyTranscoded) {
		t.Fatalf("transcode of an already transcoded recording: %v", err)
	}
	if enc.encodes != 0 {
		t.Error("it encoded a recording that was already transcoded")
	}
	if _, err := os.Stat(s.File(id, FormatAVI)); !os.IsNotExist(err) {
		t.Error("the leftover AVI was not cleared")
	}
	// One recording, not two.
	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("listed %d recordings for one identity: %+v", len(recs), recs)
	}
}

func TestBothFormsAreOneRecording(t *testing.T) {
	s := open(t, 0)
	id := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, id, aviBytes(t, 12))
	if err := os.WriteFile(s.File(id, FormatMP4), []byte("frame\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	recs, _, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].Format != FormatMP4 {
		t.Fatalf("an interrupted transcode lists as %+v", recs)
	}
	// The puller must see it as held under either name, or it downloads it
	// again for as long as the card holds it.
	held, err := s.Held("a1b2", "2026-08-30")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if !held["131529"] {
		t.Error("a transcoded recording is not in the day listing the puller reads")
	}
}

func TestUntranscodedIsNewestFirstAndSkipsWhatIsDone(t *testing.T) {
	s := open(t, 0)
	ids := []ID{
		{CameraID: "a1b2", Day: "2026-08-29", At: "090000"},
		{CameraID: "a1b2", Day: "2026-08-30", At: "131529"},
		{CameraID: "a1b2", Day: "2026-08-30", At: "180000"},
	}
	for _, id := range ids {
		save(t, s, id, aviBytes(t, 5))
	}
	if _, err := s.Transcode(context.Background(), ids[2], matching(t, s, ids[2])); err != nil {
		t.Fatalf("transcode: %v", err)
	}

	left, err := s.Untranscoded(10)
	if err != nil {
		t.Fatalf("untranscoded: %v", err)
	}
	if len(left) != 2 || left[0] != ids[1] || left[1] != ids[0] {
		t.Fatalf("wanted the two AVIs newest first, got %v", left)
	}
	if one, err := s.Untranscoded(1); err != nil || len(one) != 1 {
		t.Fatalf("limit ignored: %v %v", one, err)
	}
}

func TestRetentionRemovesTranscodedRecordings(t *testing.T) {
	s := open(t, 1)
	older := ID{CameraID: "a1b2", Day: "2026-08-29", At: "090000"}
	newer := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	save(t, s, older, aviBytes(t, 5))
	save(t, s, newer, aviBytes(t, 5))
	if _, err := s.Transcode(context.Background(), older, matching(t, s, older)); err != nil {
		t.Fatalf("transcode: %v", err)
	}

	removed, freed, err := s.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 || freed == 0 {
		t.Fatalf("swept %d recordings freeing %d bytes", removed, freed)
	}
	if s.Has(older) {
		t.Error("the aged-out MP4 is still held")
	}
	if !s.Has(newer) {
		t.Error("the newest recording was removed")
	}
}

func TestUsageCountsBothForms(t *testing.T) {
	s := open(t, 0)
	a := ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	b := ID{CameraID: "a1b2", Day: "2026-08-30", At: "180000"}
	save(t, s, a, aviBytes(t, 5))
	save(t, s, b, aviBytes(t, 5))
	if _, err := s.Transcode(context.Background(), a, matching(t, s, a)); err != nil {
		t.Fatalf("transcode: %v", err)
	}

	usage, err := s.Usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Recordings != 2 || usage.Transcoded != 1 {
		t.Errorf("usage says %d recordings, %d transcoded", usage.Recordings, usage.Transcoded)
	}
}

// assertNoTemps checks that a failed transcode left nothing behind to pay disk
// for. The archive sweeps these at startup, but a queue working through two
// hundred recordings must not need a restart to stop growing.
func assertNoTemps(t *testing.T, s *Store, id ID) {
	t.Helper()
	entries, err := os.ReadDir(s.dayDir(id.CameraID, id.Day))
	if err != nil {
		t.Fatalf("read day: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("a failed transcode left %s behind", e.Name())
		}
	}
}
