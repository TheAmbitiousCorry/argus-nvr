package avi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A held recording is replayed rather than played: nothing decodes MJPEG in a
// browser, so the service hands out the frames one at a time and the timing
// with them. That needs two things out of the file that a plain download does
// not: where every frame sits, and when it belongs.
//
// Both are in the file already. The index the writer put at the end says where
// each frame starts and how long it is, and the stream header says what rate
// the recording actually ran at, measured rather than assumed. Reading those is
// a few kilobytes; reading the frames is twelve megabytes, and several people
// may be watching at once, so nothing here reads a frame.

// maxIndexBytes bounds the index read into memory. At sixteen bytes an entry
// this is the maxFrames a writer will produce, and a file claiming more is
// claiming something no writer here made.
const maxIndexBytes = indexEntryBytes * maxFrames

// maxChunkNesting bounds the walk through the file's lists, so a file whose
// sizes point at each other cannot be walked forever.
const maxChunkNesting = 8

// defaultFrameSize is what a frame with no readable start-of-frame marker is
// reported as, matching the writer's own fallback.
const (
	defaultWidth  = 640
	defaultHeight = 480
)

// jpegHeadBytes is how much of the first frame is read to find its dimensions.
// The start-of-frame marker sits after the quantisation and Huffman tables,
// which is well inside this on anything these cameras produce.
const jpegHeadBytes = 4 << 10

// Frame is one JPEG in a recording: where its bytes are, how many there are,
// and how far into the recording it belongs.
type Frame struct {
	// Offset is into the file, of the JPEG itself rather than its chunk header,
	// so a reader can seek straight to the frame.
	Offset int64
	Length int
	// AtMs is milliseconds from the start of the recording.
	AtMs int64
}

// Index is everything a replay needs that is not a frame.
//
// Width and height come from the first frame rather than from the header,
// because footage made before a resolution change is still held and the header
// is only as right as the writer that filled it in. It is the same rule the
// firmware follows when it serves a recording off its own card.
type Index struct {
	Frames []Frame
	Width  uint16
	Height uint16
	// DurMs is the recording's own length: the frame count at the rate the
	// header states, which is the rate the recording was measured to run at.
	DurMs int64
	// MaxFrameLen is the largest frame, so a replay can size one buffer once
	// and reuse it for every frame it sends.
	MaxFrameLen int
}

// ErrNotAVI means the file is not one of ours, or not an AVI at all.
var ErrNotAVI = errors.New("avi: not an AVI file")

// ReadIndex reads a recording's frame index without reading its frames.
func ReadIndex(r io.ReaderAt, size int64) (Index, error) {
	var head [12]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return Index{}, ErrNotAVI
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "AVI " {
		return Index{}, ErrNotAVI
	}
	// The file states its own length. Trusting the file over the disk would
	// let a truncated recording send a reader past the end of it.
	if stated := int64(binary.LittleEndian.Uint32(head[4:8])) + 8; stated < size {
		size = stated
	}

	f := finder{r: r, size: size}
	if err := f.walk(12, size, 0); err != nil {
		return Index{}, err
	}
	if f.moviAt == 0 {
		return Index{}, fmt.Errorf("avi: no movie list")
	}

	frames, err := f.frames()
	if err != nil {
		return Index{}, err
	}
	if len(frames) == 0 {
		return Index{}, errors.New("avi: recording holds no frames")
	}

	idx := Index{Frames: frames}
	for i := range frames {
		if frames[i].Length > idx.MaxFrameLen {
			idx.MaxFrameLen = frames[i].Length
		}
	}
	f.time(idx.Frames)
	idx.DurMs = f.durationMs(len(frames))
	idx.Width, idx.Height = f.frameSize(frames[0])
	return idx, nil
}

// finder is one pass over the file's chunk structure, holding where the parts
// that matter turned out to be.
type finder struct {
	r    io.ReaderAt
	size int64

	// moviAt is the offset of the 'movi' tag itself, which is what the index
	// measures its own offsets from.
	moviAt  int64
	moviEnd int64

	idxAt    int64
	idxBytes int64

	// usPerFrame is the rate the movie header states, and scale over rate the
	// finer one the stream header states. A rate as a fraction survives the odd
	// frame rates a camera that drops frames actually produces; the whole
	// microseconds in the movie header do not.
	usPerFrame  uint32
	scale, rate uint32

	avihWidth, avihHeight uint16
}

// walk reads the chunks between at and end, descending into the lists that hold
// something worth having.
func (f *finder) walk(at, end int64, depth int) error {
	if depth > maxChunkNesting {
		return nil
	}
	for at+8 <= end {
		var head [8]byte
		if _, err := f.r.ReadAt(head[:], at); err != nil {
			return nil
		}
		id := string(head[0:4])
		size := int64(binary.LittleEndian.Uint32(head[4:8]))
		body := at + 8
		if size < 0 || body+size > end {
			// A chunk that runs past its parent is a truncated file. What was
			// read before it is still worth having.
			size = end - body
		}

		switch id {
		case "LIST":
			if body+4 > end {
				return nil
			}
			var kind [4]byte
			if _, err := f.r.ReadAt(kind[:], body); err != nil {
				return nil
			}
			switch string(kind[:]) {
			case "movi":
				f.moviAt = body
				f.moviEnd = body + size
			case "hdrl", "strl":
				if err := f.walk(body+4, body+size, depth+1); err != nil {
					return err
				}
			}
		case "idx1":
			f.idxAt, f.idxBytes = body, size
		case "avih":
			f.readAvih(body, size)
		case "strh":
			f.readStrh(body, size)
		}

		// A chunk is padded to an even length, and the pad byte is not counted
		// in the size, which is the one place this format punishes a careless
		// reader.
		at = body + size + (size & 1)
	}
	return nil
}

func (f *finder) readAvih(at, size int64) {
	if size < 40 {
		return
	}
	buf := make([]byte, 40)
	if _, err := f.r.ReadAt(buf, at); err != nil {
		return
	}
	f.usPerFrame = binary.LittleEndian.Uint32(buf[0:])
	f.avihWidth = uint16(binary.LittleEndian.Uint32(buf[32:]))
	f.avihHeight = uint16(binary.LittleEndian.Uint32(buf[36:]))
}

func (f *finder) readStrh(at, size int64) {
	if size < 32 {
		return
	}
	buf := make([]byte, 32)
	if _, err := f.r.ReadAt(buf, at); err != nil {
		return
	}
	if string(buf[0:4]) != "vids" {
		return
	}
	f.scale = binary.LittleEndian.Uint32(buf[20:])
	f.rate = binary.LittleEndian.Uint32(buf[24:])
}

// frames reads the index the writer left at the end of the file, and falls back
// to walking the movie list when there is none to read. The index is the cheap
// way and the walk is the one that still works on a file whose index is gone.
func (f *finder) frames() ([]Frame, error) {
	if frames := f.fromIndex(); len(frames) > 0 {
		return frames, nil
	}
	return f.fromMovie(), nil
}

func (f *finder) fromIndex() []Frame {
	if f.idxAt == 0 || f.idxBytes < indexEntryBytes {
		return nil
	}
	n := f.idxBytes
	if n > maxIndexBytes {
		n = maxIndexBytes
	}
	buf := make([]byte, n-n%indexEntryBytes)
	if _, err := f.r.ReadAt(buf, f.idxAt); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}

	base, ok := f.indexBase(buf)
	if !ok {
		return nil
	}

	frames := make([]Frame, 0, len(buf)/indexEntryBytes)
	for at := 0; at+indexEntryBytes <= len(buf); at += indexEntryBytes {
		e := buf[at:]
		length := int(binary.LittleEndian.Uint32(e[12:]))
		if length <= 0 {
			continue
		}
		// The offset in an entry is to the frame's chunk header; the frame
		// itself starts eight bytes later.
		off := base + int64(binary.LittleEndian.Uint32(e[8:])) + 8
		if off < 0 || off+int64(length) > f.size {
			return nil
		}
		frames = append(frames, Frame{Offset: off, Length: length})
	}
	return frames
}

// indexBase works out what the index measured its offsets from.
//
// The format allows both, and both are in the wild: this writer and the
// firmware measure from the 'movi' tag, and other writers measure from the
// start of the file. The file says which by whether a chunk header is actually
// where the entry claims, so it is asked rather than assumed.
func (f *finder) indexBase(entries []byte) (int64, bool) {
	for at := 0; at+indexEntryBytes <= len(entries); at += indexEntryBytes {
		e := entries[at:]
		if binary.LittleEndian.Uint32(e[12:]) == 0 {
			continue
		}
		want := string(e[0:4])
		off := int64(binary.LittleEndian.Uint32(e[8:]))
		for _, base := range []int64{f.moviAt, 0} {
			if f.chunkAt(base+off, want) {
				return base, true
			}
		}
		return 0, false
	}
	return 0, false
}

func (f *finder) chunkAt(at int64, want string) bool {
	if at < 0 || at+8 > f.size {
		return false
	}
	var head [4]byte
	if _, err := f.r.ReadAt(head[:], at); err != nil {
		return false
	}
	return string(head[:]) == want
}

// fromMovie walks the movie list one chunk header at a time. It costs eight
// bytes a frame rather than the sixteen the index holds, and it is what answers
// for a recording whose index did not survive.
func (f *finder) fromMovie() []Frame {
	var frames []Frame
	at := f.moviAt + 4
	end := f.moviEnd
	if end > f.size {
		end = f.size
	}
	for at+8 <= end && len(frames) < maxFrames {
		var head [8]byte
		if _, err := f.r.ReadAt(head[:], at); err != nil {
			break
		}
		size := int64(binary.LittleEndian.Uint32(head[4:8]))
		if size < 0 || at+8+size > end {
			break
		}
		if string(head[2:4]) == "dc" && size > 0 {
			frames = append(frames, Frame{Offset: at + 8, Length: int(size)})
		}
		at += 8 + size + (size & 1)
	}
	return frames
}

// time stamps every frame from the rate the file states.
//
// The rate is the one number a run of JPEGs cannot supply, and it is why these
// recordings are wrapped in a container at all. Both writers measure it from
// the clock rather than assuming one, so a recording that ran at eight frames a
// second because the radio was busy replays over the length it actually took
// rather than being rushed through at twenty five.
func (f *finder) time(frames []Frame) {
	us := f.usPerFrameExact()
	for i := range frames {
		frames[i].AtMs = int64(i) * us / 1000
	}
}

// usPerFrameExact prefers the stream header's fraction over the movie header's
// whole microseconds: the fraction is frames over milliseconds and survives the
// division, and the microseconds have already lost up to a part in a thousand.
func (f *finder) usPerFrameExact() int64 {
	if f.rate > 0 && f.scale > 0 {
		if us := int64(f.scale) * 1000000 / int64(f.rate); us > 0 && us <= int64(maxFrameGapUs) {
			return us
		}
	}
	if f.usPerFrame > 0 && int64(f.usPerFrame) <= int64(maxFrameGapUs) {
		return int64(f.usPerFrame)
	}
	return defaultUsPerFrame
}

// maxFrameGapUs rejects a stated rate that would replay a ten second clip over
// hours. Ten seconds a frame is already far slower than anything these cameras
// produce, so past it the header is wrong rather than unusual.
const maxFrameGapUs = 10 * 1000 * 1000

func (f *finder) durationMs(frames int) int64 {
	return int64(frames) * f.usPerFrameExact() / 1000
}

// size reads the first frame's start-of-frame marker.
//
// The recording says what it was made at, rather than the camera saying what it
// is set to now: footage from before a resolution change is still held, and
// describing it by today's setting is a bug the firmware already had once.
func (f *finder) frameSize(first Frame) (uint16, uint16) {
	n := first.Length
	if n > jpegHeadBytes {
		n = jpegHeadBytes
	}
	buf := make([]byte, n)
	if _, err := f.r.ReadAt(buf, first.Offset); err == nil || errors.Is(err, io.EOF) {
		if w, h, ok := JPEGSize(buf); ok {
			return w, h
		}
	}
	if f.avihWidth > 0 && f.avihHeight > 0 {
		return f.avihWidth, f.avihHeight
	}
	return defaultWidth, defaultHeight
}
