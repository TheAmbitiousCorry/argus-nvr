package avi

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// jpegOf builds the smallest thing that is still a JPEG with readable
// dimensions: a start-of-image, a baseline start-of-frame carrying the size,
// and an end-of-image, padded to the length asked for.
func jpegOf(w, h uint16, pad int) []byte {
	out := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08}
	out = append(out, byte(h>>8), byte(h), byte(w>>8), byte(w), 0x03)
	out = append(out, make([]byte, 9)...)
	out = append(out, make([]byte, pad)...)
	return append(out, 0xFF, 0xD9)
}

func writeTo(t *testing.T, frames [][]byte, gap time.Duration) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.avi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	at := time.Unix(1700000000, 0)
	for _, frame := range frames {
		if err := w.WriteFrame(frame, at); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		at = at.Add(gap)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return data
}

func u32(b []byte, at int) uint32 { return binary.LittleEndian.Uint32(b[at:]) }

// The file must say it is exactly as long as it is. This is the check a
// download does at the far end, so a writer that got it wrong would have every
// recording it made rejected as truncated.
func TestFileStatesItsOwnLength(t *testing.T) {
	frames := [][]byte{jpegOf(640, 480, 100), jpegOf(640, 480, 101), jpegOf(640, 480, 7)}
	data := writeTo(t, frames, 40*time.Millisecond)

	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "AVI " {
		t.Fatalf("not an AVI: % x", data[0:12])
	}
	if got, want := int(u32(data, 4))+8, len(data); got != want {
		t.Errorf("RIFF says the file is %d bytes, it is %d", got, want)
	}
}

// An index entry that points at the wrong place is the classic AVI mistake: the
// pad byte after an odd length frame is not counted in the chunk size but every
// offset after it moves. Every entry is checked against what is actually there.
func TestIndexPointsAtEveryFrame(t *testing.T) {
	frames := [][]byte{jpegOf(640, 480, 100), jpegOf(640, 480, 101), jpegOf(320, 240, 7)}
	data := writeTo(t, frames, 40*time.Millisecond)

	// The index sits after the header and the frames.
	var movie int
	for _, f := range frames {
		movie += 8 + len(f) + len(f)%2
	}
	idx := HeaderBytes + movie
	if string(data[idx:idx+4]) != "idx1" {
		t.Fatalf("no index where one was expected: % x", data[idx:idx+4])
	}
	if got, want := u32(data, idx+4), uint32(indexEntryBytes*len(frames)); got != want {
		t.Fatalf("index is %d bytes, wanted %d", got, want)
	}

	// The movi tag is the last four bytes of the header, and index offsets are
	// measured from it.
	movi := HeaderBytes - 4
	for i, frame := range frames {
		e := idx + indexHeaderBytes + indexEntryBytes*i
		if string(data[e:e+4]) != "00dc" {
			t.Errorf("frame %d: index entry is not a frame: % x", i, data[e:e+4])
		}
		at := movi + int(u32(data, e+8))
		if string(data[at:at+4]) != "00dc" {
			t.Errorf("frame %d: index points at % x, not a frame header", i, data[at:at+4])
		}
		if got, want := u32(data, at+4), uint32(len(frame)); got != want {
			t.Errorf("frame %d: chunk says %d bytes, frame is %d", i, got, want)
		}
		if got, want := u32(data, e+12), uint32(len(frame)); got != want {
			t.Errorf("frame %d: index says %d bytes, frame is %d", i, got, want)
		}
		if string(data[at+8:at+8+len(frame)]) != string(frame) {
			t.Errorf("frame %d: bytes at the indexed offset are not the frame", i)
		}
	}
}

// The rate has to come from the clock. A header claiming a rate the recording
// never ran at puts the end of playback somewhere other than where the
// recording ended, which is what someone checking a timestamp will notice.
func TestRateIsMeasuredNotAssumed(t *testing.T) {
	for _, gap := range []time.Duration{100 * time.Millisecond, 41 * time.Millisecond} {
		frames := make([][]byte, 11)
		for i := range frames {
			frames[i] = jpegOf(640, 480, 50)
		}
		data := writeTo(t, frames, gap)

		// avih holds microseconds per frame at a fixed offset: RIFF, the header
		// list opening, then the avih tag and its size.
		const usPerFrameAt = 12 + 12 + 8
		if got, want := u32(data, usPerFrameAt), uint32(gap.Microseconds()); got != want {
			t.Errorf("gap %s: header says %dus a frame, frames were %dus apart", gap, got, want)
		}
	}
}

// A clip of one frame has no measurable rate, and must still be a file rather
// than a division by zero.
func TestOneFrameStillWrites(t *testing.T) {
	data := writeTo(t, [][]byte{jpegOf(160, 120, 3)}, 0)
	if got, want := int(u32(data, 4))+8, len(data); got != want {
		t.Errorf("RIFF says %d bytes, file is %d", got, want)
	}
	const usPerFrameAt = 12 + 12 + 8
	if got := u32(data, usPerFrameAt); got != defaultUsPerFrame {
		t.Errorf("one frame: %dus a frame, wanted the fallback %d", got, defaultUsPerFrame)
	}
}

// The size comes from the footage, not from whatever the camera is set to now,
// because recordings made at an earlier resolution are still on the card.
func TestSizeComesFromTheFrame(t *testing.T) {
	data := writeTo(t, [][]byte{jpegOf(1024, 768, 20), jpegOf(1024, 768, 20)}, 40*time.Millisecond)
	const widthAt = 12 + 12 + 8 + 32
	if got := u32(data, widthAt); got != 1024 {
		t.Errorf("header width %d, wanted 1024", got)
	}
	if got := u32(data, widthAt+4); got != 768 {
		t.Errorf("header height %d, wanted 768", got)
	}
}

func TestJPEGSize(t *testing.T) {
	w, h, ok := JPEGSize(jpegOf(640, 480, 0))
	if !ok || w != 640 || h != 480 {
		t.Errorf("read %dx%d ok=%v, wanted 640x480", w, h, ok)
	}
	// Anything that is not a JPEG must be refused rather than guessed at.
	if _, _, ok := JPEGSize([]byte("not a jpeg at all, not even close")); ok {
		t.Error("read a size out of something that is not a JPEG")
	}
}

// A recording with no frames is not a file worth having.
func TestNoFramesIsAnError(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "empty.avi"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := w.Close(); err == nil {
		t.Error("closing a writer that was given no frames succeeded")
	}
}
