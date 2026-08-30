package transcode

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/avi"
)

// These tests run against the real ffmpeg when there is one, and skip when
// there is not. That is the same rule the service follows: a machine without
// ffmpeg is a machine that holds AVIs, not a machine where something is broken.
func ffmpegOrSkip(t *testing.T, crf int) *FFmpeg {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	enc, err := Find("ffmpeg", crf)
	if err != nil {
		t.Fatalf("find ffmpeg: %v", err)
	}
	return enc
}

// mjpegAVI writes a real recording: n frames of a moving square, JPEG encoded,
// in the AVI the cameras and this service both write. Something has to move,
// or H.264 encodes the whole thing as one still and the frame count is the only
// thing left being tested.
func mjpegAVI(t *testing.T, dir string, n int) string {
	t.Helper()
	path := filepath.Join(dir, "clip.avi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	w, err := avi.NewWriter(f)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	at := time.Unix(1700000000, 0)
	for i := 0; i < n; i++ {
		img := image.NewGray(image.Rect(0, 0, 96, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 96; x++ {
				shade := uint8((x*3 + y*5 + i*17) % 256)
				img.SetGray(x, y, color.Gray{Y: shade})
			}
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatalf("jpeg: %v", err)
		}
		if err := w.WriteFrame(buf.Bytes(), at); err != nil {
			t.Fatalf("frame: %v", err)
		}
		at = at.Add(40 * time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

func TestEncodeKeepsEveryFrame(t *testing.T) {
	enc := ffmpegOrSkip(t, DefaultCRF)
	dir := t.TempDir()
	src := mjpegAVI(t, dir, 40)
	dst := filepath.Join(dir, "out.mp4")

	if err := enc.Encode(context.Background(), src, dst); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := enc.Frames(context.Background(), dst)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	if got != 40 {
		t.Errorf("the encode holds %d frames, the recording holds 40", got)
	}
	// The output is an MP4 whatever the file is called, because the temp name
	// the archive hands over has no extension to go on.
	head, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		t.Errorf("the output is not an MP4: % x", head[:min(12, len(head))])
	}
}

func TestATruncatedEncodeIsCaught(t *testing.T) {
	enc := ffmpegOrSkip(t, DefaultCRF)
	dir := t.TempDir()
	src := mjpegAVI(t, dir, 40)
	dst := filepath.Join(dir, "out.mp4")
	if err := enc.Encode(context.Background(), src, dst); err != nil {
		t.Fatalf("encode: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// A write that stopped part way, which is what a full disk or a killed
	// process leaves behind.
	if err := os.Truncate(dst, info.Size()/2); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	got, err := enc.Frames(context.Background(), dst)
	if err == nil && got == 40 {
		t.Fatal("a half written encode was counted as whole")
	}
}

func TestACorruptedEncodeIsCaught(t *testing.T) {
	enc := ffmpegOrSkip(t, DefaultCRF)
	dir := t.TempDir()
	src := mjpegAVI(t, dir, 40)
	dst := filepath.Join(dir, "out.mp4")
	if err := enc.Encode(context.Background(), src, dst); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Rot in the middle of the video data, past the header, which is the
	// corruption a header check would miss.
	for i := len(data) / 2; i < len(data)/2+512 && i < len(data); i++ {
		data[i] ^= 0xFF
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := enc.Frames(context.Background(), dst); err == nil {
		t.Fatal("a corrupted encode decoded without complaint")
	}
}

// The whole point of the archive's rule, proved end to end with a real encoder:
// a recording that will not survive being read back keeps its AVI.
func TestArchiveKeepsTheAVIWhenTheRealEncodeIsBroken(t *testing.T) {
	enc := ffmpegOrSkip(t, DefaultCRF)
	arch, err := archive.Open(filepath.Join(t.TempDir(), "recordings"), 0)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	id := archive.ID{CameraID: "a1b2", Day: "2026-08-30", At: "131529"}
	data, err := os.ReadFile(mjpegAVI(t, t.TempDir(), 40))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := arch.Save(id, bytes.NewReader(data), archive.Meta{Source: archive.SourceCamera}); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, err := arch.Transcode(context.Background(), id, enc)
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	if out.Frames != 40 {
		t.Errorf("verified %d frames against 40", out.Frames)
	}
	if out.After >= out.Before {
		t.Errorf("the MP4 is %d bytes against the AVI's %d", out.After, out.Before)
	}
	t.Logf("%d bytes to %d bytes, %.1fx, in %s", out.Before, out.After, out.Ratio(), out.Took)

	// And the same recording, encoded by something that lies about what it
	// produced, keeps its AVI.
	other := archive.ID{CameraID: "a1b2", Day: "2026-08-30", At: "140000"}
	if _, err := arch.Save(other, bytes.NewReader(data), archive.Meta{Source: archive.SourceCamera}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := arch.Transcode(context.Background(), other, truncating{enc}); err == nil {
		t.Fatal("a truncated encode replaced the recording")
	}
	if _, _, format, err := arch.Open(other); err != nil || format != archive.FormatAVI {
		t.Errorf("after a broken encode the recording is %q (%v)", format, err)
	}
}

// truncating is a real encoder whose output is cut in half on the way out, the
// way a full disk would.
type truncating struct{ *FFmpeg }

func (t truncating) Encode(ctx context.Context, src, dst string) error {
	if err := t.FFmpeg.Encode(ctx, src, dst); err != nil {
		return err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return err
	}
	return os.Truncate(dst, info.Size()/2)
}

func TestFindRefusesAnImpossibleCRF(t *testing.T) {
	if _, err := Find("ffmpeg", 99); err == nil {
		t.Error("crf 99 was accepted")
	}
}

func TestFindSaysWhenThereIsNoFFmpeg(t *testing.T) {
	_, err := Find(filepath.Join(t.TempDir(), "not-ffmpeg"), DefaultCRF)
	if err == nil {
		t.Fatal("a missing binary was found")
	}
	if !errors.Is(err, ErrNoFFmpeg) {
		t.Errorf("wanted ErrNoFFmpeg, got %v", err)
	}
}
