package avi

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// The index the writer left has to lead back to the exact bytes of every frame.
// This is the whole basis of replaying a recording without reading it: a reader
// seeks to what the index says and sends what it finds, so an offset that is
// out by the eight bytes of a chunk header, or by a pad byte, sends a viewer
// something that is not a JPEG.
func TestIndexLeadsToEveryFrame(t *testing.T) {
	frames := [][]byte{
		jpegOf(800, 600, 101), // odd, so the next frame is past a pad byte
		jpegOf(800, 600, 40),
		jpegOf(800, 600, 7),
		jpegOf(800, 600, 512),
	}
	data := writeTo(t, frames, 40*time.Millisecond)

	idx, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx.Frames) != len(frames) {
		t.Fatalf("indexed %d frames, wrote %d", len(idx.Frames), len(frames))
	}
	for i, f := range idx.Frames {
		if f.Length != len(frames[i]) {
			t.Errorf("frame %d is %d bytes in the index, %d as written", i, f.Length, len(frames[i]))
		}
		got := data[f.Offset : f.Offset+int64(f.Length)]
		if !bytes.Equal(got, frames[i]) {
			t.Errorf("frame %d at offset %d is not the frame that was written", i, f.Offset)
		}
	}
	if idx.MaxFrameLen != len(frames[3]) {
		t.Errorf("MaxFrameLen = %d, want %d", idx.MaxFrameLen, len(frames[3]))
	}
}

// The index's offsets are measured from the 'movi' tag rather than from the
// start of the file, which is what both writers here do and what the format
// also allows to be done the other way. The reader has to work out which by
// asking the file, so a recording written either way replays.
func TestIndexOffsetsAreReadRelativeToTheMovieList(t *testing.T) {
	data := writeTo(t, [][]byte{jpegOf(640, 480, 20), jpegOf(640, 480, 21)}, 40*time.Millisecond)

	// The first entry's offset is four: the width of the 'movi' tag it is
	// measured from. Read as a file offset it would land in the header.
	idxAt := bytes.LastIndex(data, []byte("idx1"))
	if idxAt < 0 {
		t.Fatal("no index in the file")
	}
	if off := binary.LittleEndian.Uint32(data[idxAt+8+8:]); off != firstFrameOffset {
		t.Fatalf("first index entry says offset %d, want %d", off, firstFrameOffset)
	}

	idx, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx.Frames[0].Offset != HeaderBytes+8 {
		t.Errorf("first frame is at %d, want %d", idx.Frames[0].Offset, HeaderBytes+8)
	}
}

// A recording whose index did not survive is still footage. Walking the movie
// list costs one read of eight bytes a frame, which is cheaper than telling
// somebody their recording is gone.
func TestARecordingWithNoIndexIsStillReadable(t *testing.T) {
	frames := [][]byte{jpegOf(640, 480, 31), jpegOf(640, 480, 30), jpegOf(640, 480, 9)}
	data := writeTo(t, frames, 40*time.Millisecond)

	cut := bytes.LastIndex(data, []byte("idx1"))
	if cut < 0 {
		t.Fatal("no index in the file")
	}
	truncated := data[:cut]

	idx, err := ReadIndex(bytes.NewReader(truncated), int64(len(truncated)))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx.Frames) != len(frames) {
		t.Fatalf("walked %d frames, wrote %d", len(idx.Frames), len(frames))
	}
	for i, f := range idx.Frames {
		if !bytes.Equal(truncated[f.Offset:f.Offset+int64(f.Length)], frames[i]) {
			t.Errorf("frame %d is not the frame that was written", i)
		}
	}
}

// The size a recording was made at is in its frames, not in its header.
// Recordings on disk were made at whatever the camera was set to at the time,
// and describing one by a header that says something else is a bug the firmware
// already had to fix once.
func TestSizeComesFromTheFrameRatherThanTheHeader(t *testing.T) {
	data := writeTo(t, [][]byte{jpegOf(800, 600, 10), jpegOf(800, 600, 10)}, 40*time.Millisecond)

	// Rewrite the movie header to claim the camera's current setting rather
	// than the one the footage was made at.
	binary.LittleEndian.PutUint32(data[64:], 1600)
	binary.LittleEndian.PutUint32(data[68:], 1200)

	idx, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx.Width != 800 || idx.Height != 600 {
		t.Errorf("read %dx%d, want the 800x600 the frames were made at", idx.Width, idx.Height)
	}
}

// Frames are timed from the rate the file states, which both writers measured
// from a clock rather than assumed. A recording that ran at eight frames a
// second because the radio was busy has to replay over the length it took.
func TestFramesAreTimedFromTheRateTheFileStates(t *testing.T) {
	const gap = 125 * time.Millisecond // eight frames a second
	frames := make([][]byte, 9)
	for i := range frames {
		frames[i] = jpegOf(640, 480, 16)
	}
	data := writeTo(t, frames, gap)

	idx, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	for i, f := range idx.Frames {
		want := int64(i) * gap.Milliseconds()
		if f.AtMs != want {
			t.Errorf("frame %d is at %dms, want %dms", i, f.AtMs, want)
		}
	}
	if want := int64(len(frames)) * gap.Milliseconds(); idx.DurMs != want {
		t.Errorf("DurMs = %d, want %d", idx.DurMs, want)
	}
}

func TestSomethingThatIsNotAnAVIIsRefused(t *testing.T) {
	for _, data := range [][]byte{
		{},
		[]byte("not a recording at all"),
		append([]byte("RIFF\x10\x00\x00\x00WAVE"), make([]byte, 16)...),
	} {
		if _, err := ReadIndex(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("ReadIndex(%q) succeeded, want an error", data)
		}
	}
}
