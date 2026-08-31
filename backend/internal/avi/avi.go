// Package avi wraps a run of JPEG frames in an AVI container.
//
// This is the same job the firmware does in argus-cam/src/avi.cpp, and the
// bytes it writes are the same bytes, so a recording the service made on a
// camera's behalf and one it pulled off that camera's card play identically. A
// bare run of JPEGs carries no timing at all: mpv shows one as a folder of
// photos at a frame a second, ffmpeg assumes twenty five, and a twelve second
// clip plays as eighty five seconds or as three. AVI is the cheapest container
// that fixes that, because it stores the frames exactly as they already are.
//
// The one number the frames cannot supply is the rate. It is measured from the
// clock rather than assumed, because these cameras deliver whatever the radio
// leaves room for and a header claiming a rate the recording never ran at puts
// the end of playback somewhere other than where the recording ended.
package avi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// HeaderBytes is the fixed length of everything before the first frame: RIFF,
// the header list, and the opening of the movie list. Being fixed is what lets
// a writer reserve the space, stream the frames straight through, and come back
// to fill it in once the frame count and the elapsed time are known.
const HeaderBytes = 224

const (
	indexHeaderBytes = 8
	indexEntryBytes  = 16
	// firstFrameOffset is where the first frame's chunk header sits, measured
	// the way the index wants it: from the 'movi' tag rather than the file.
	firstFrameOffset = 4
)

// defaultUsPerFrame stands in for a recording too short to measure a rate from,
// and matches the firmware's own fallback of twenty five frames a second.
const defaultUsPerFrame = 40000

// maxFrames bounds a single recording so a camera that never says it has
// stopped cannot grow one file until the disk is gone. At the measured 24fps
// this is a little over two hours.
const maxFrames = 200000

// ErrTooManyFrames ends a recording that has run past maxFrames.
var ErrTooManyFrames = errors.New("avi: recording is longer than one file should hold")

// Writer streams frames into an AVI as they arrive.
//
// Frames are written where they land and the header is rewritten at the end,
// so nothing is buffered and a recording of any length costs four bytes of
// memory a frame. That does mean the file is only playable once Close has run,
// which is why callers write to a temp file and rename.
type Writer struct {
	w io.WriteSeeker

	lens        []uint32
	movieBytes  uint32
	maxFrameLen uint32
	width       uint16
	height      uint16

	first time.Time
	last  time.Time
}

// NewWriter reserves the header and leaves w positioned at the first frame.
func NewWriter(w io.WriteSeeker) (*Writer, error) {
	if _, err := w.Write(make([]byte, HeaderBytes)); err != nil {
		return nil, fmt.Errorf("avi: reserve header: %w", err)
	}
	return &Writer{w: w}, nil
}

// WriteFrame appends one JPEG, timed by at. The times of the first and last
// frames are the whole basis of the rate in the header, so they come from the
// caller's clock rather than from anything in the frame.
func (a *Writer) WriteFrame(jpeg []byte, at time.Time) error {
	if len(jpeg) == 0 {
		return nil
	}
	if len(a.lens) >= maxFrames {
		return ErrTooManyFrames
	}
	if a.width == 0 {
		// The footage says what size it was made at. A camera whose resolution
		// changed mid-recording is not worth handling, but one that changed
		// between recordings is, and asking the frame costs nothing.
		w, h, ok := JPEGSize(jpeg)
		if !ok {
			w, h = 640, 480
		}
		a.width, a.height = w, h
		a.first = at
	}
	a.last = at

	n := uint32(len(jpeg))
	var head [8]byte
	copy(head[:4], "00dc")
	binary.LittleEndian.PutUint32(head[4:], n)
	if _, err := a.w.Write(head[:]); err != nil {
		return err
	}
	if _, err := a.w.Write(jpeg); err != nil {
		return err
	}
	// A chunk is padded to an even length. The pad byte is not counted in the
	// chunk's own size, but everything after it is offset by it, which is the
	// one place this format punishes a careless writer.
	if n&1 == 1 {
		if _, err := a.w.Write([]byte{0}); err != nil {
			return err
		}
	}

	a.lens = append(a.lens, n)
	a.movieBytes += 8 + n + (n & 1)
	if n > a.maxFrameLen {
		a.maxFrameLen = n
	}
	return nil
}

// Frames reports how many frames have been written.
func (a *Writer) Frames() int { return len(a.lens) }

// Duration is the span the frames were received over, which is what the header
// rate is derived from.
func (a *Writer) Duration() time.Duration {
	if len(a.lens) < 2 {
		return 0
	}
	return a.last.Sub(a.first)
}

// Close writes the index and rewrites the header. A file whose Close did not
// run holds a header of zeroes and will not play, which is the point: it is
// indistinguishable from an interrupted write, and both are thrown away.
func (a *Writer) Close() error {
	if len(a.lens) == 0 {
		return errors.New("avi: no frames were written")
	}

	idx := make([]byte, indexHeaderBytes+indexEntryBytes*len(a.lens))
	copy(idx[:4], "idx1")
	binary.LittleEndian.PutUint32(idx[4:], uint32(indexEntryBytes*len(a.lens)))
	off := uint32(firstFrameOffset)
	for i, n := range a.lens {
		e := idx[indexHeaderBytes+indexEntryBytes*i:]
		copy(e[:4], "00dc")
		binary.LittleEndian.PutUint32(e[4:], 0x10) // every frame is a key frame; JPEG has no other kind
		binary.LittleEndian.PutUint32(e[8:], off)
		binary.LittleEndian.PutUint32(e[12:], n)
		off += 8 + n + (n & 1)
	}
	if _, err := a.w.Write(idx); err != nil {
		return err
	}

	if _, err := a.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	head := make([]byte, HeaderBytes)
	writeHeader(head, info{
		frames:      uint32(len(a.lens)),
		usPerFrame:  a.usPerFrame(),
		movieBytes:  a.movieBytes,
		maxFrameLen: a.maxFrameLen,
		width:       a.width,
		height:      a.height,
	})
	_, err := a.w.Write(head)
	return err
}

// usPerFrame is the mean gap between frames, measured. The span runs from the
// first frame to the last, so it covers one gap fewer than there are frames,
// and dividing by the frame count instead would stretch every recording by a
// frame's worth: small on a thousand frames, a tenth of the length on ten.
func (a *Writer) usPerFrame() uint32 {
	if len(a.lens) < 2 {
		return defaultUsPerFrame
	}
	span := a.last.Sub(a.first)
	if span <= 0 {
		return defaultUsPerFrame
	}
	us := span.Microseconds() / int64(len(a.lens)-1)
	if us <= 0 {
		return 1
	}
	return uint32(us)
}

// info is everything the header needs that a single frame cannot say.
type info struct {
	frames      uint32
	usPerFrame  uint32
	movieBytes  uint32
	maxFrameLen uint32
	width       uint16
	height      uint16
}

// writeHeader fills exactly HeaderBytes. Every field is little endian and
// almost all of them are four byte integers or four character tags, so the
// whole format is the two putters below.
func writeHeader(out []byte, in info) {
	if in.usPerFrame == 0 {
		in.usPerFrame = defaultUsPerFrame
	}
	// strh states the rate as a fraction of frames over milliseconds, so the
	// odd rates a camera that drops frames actually produces come out exact
	// instead of rounded to a whole number of frames a second.
	durMs := uint32(uint64(in.frames) * uint64(in.usPerFrame) / 1000)
	if durMs == 0 {
		durMs = 1
	}
	movieList := 4 + in.movieBytes
	indexBytes := uint32(indexHeaderBytes + indexEntryBytes*int(in.frames))
	// 'AVI ', the header list, the movie list and the index.
	riff := 4 + 200 + 8 + movieList + indexBytes

	p := &putter{buf: out}
	p.tag("RIFF")
	p.u32(riff)
	p.tag("AVI ")

	p.tag("LIST")
	p.u32(192)
	p.tag("hdrl")

	p.tag("avih")
	p.u32(56)
	p.u32(in.usPerFrame)
	p.u32(in.maxFrameLen * (1000000 / in.usPerFrame)) // bytes a second
	p.u32(0)                                          // padding granularity
	p.u32(0x10)                                       // has an index
	p.u32(in.frames)
	p.u32(0) // initial frames
	p.u32(1) // one stream
	p.u32(in.maxFrameLen)
	p.u32(uint32(in.width))
	p.u32(uint32(in.height))
	for i := 0; i < 4; i++ {
		p.u32(0) // reserved
	}

	p.tag("LIST")
	p.u32(116)
	p.tag("strl")

	p.tag("strh")
	p.u32(56)
	p.tag("vids")
	p.tag("MJPG")
	p.u32(0) // flags
	p.u16(0) // priority
	p.u16(0) // language
	p.u32(0) // initial frames
	p.u32(durMs)
	p.u32(in.frames * 1000)
	p.u32(0) // start
	p.u32(in.frames)
	p.u32(in.maxFrameLen)
	p.u32(0xFFFFFFFF) // quality: use the codec's own
	p.u32(0)          // sample size: frames vary
	p.u16(0)
	p.u16(0)
	p.u16(in.width)
	p.u16(in.height)

	p.tag("strf")
	p.u32(40)
	p.u32(40) // size of this structure
	p.u32(uint32(in.width))
	p.u32(uint32(in.height))
	p.u16(1)  // planes
	p.u16(24) // bits per pixel
	p.tag("MJPG")
	p.u32(uint32(in.width) * uint32(in.height) * 3)
	p.u32(0) // pixels per metre, horizontal
	p.u32(0) // pixels per metre, vertical
	p.u32(0) // colours used
	p.u32(0) // colours that matter

	p.tag("LIST")
	p.u32(movieList)
	p.tag("movi")
}

type putter struct {
	buf []byte
	at  int
}

func (p *putter) u32(v uint32) {
	binary.LittleEndian.PutUint32(p.buf[p.at:], v)
	p.at += 4
}

func (p *putter) u16(v uint16) {
	binary.LittleEndian.PutUint16(p.buf[p.at:], v)
	p.at += 2
}

func (p *putter) tag(s string) {
	copy(p.buf[p.at:], s)
	p.at += 4
}

// JPEGSize reads the frame size out of a JPEG's start-of-frame marker.
//
// It walks the markers rather than searching for the 0xFFC0 byte pair, because
// that pair turns up inside compressed data often enough that searching finds
// the wrong one and reports a recording as some impossible size.
func JPEGSize(data []byte) (width, height uint16, ok bool) {
	i := 2 // past the start-of-image marker
	for i+9 < len(data) {
		if data[i] != 0xFF {
			return 0, 0, false
		}
		marker := data[i+1]
		size := int(data[i+2])<<8 | int(data[i+3])
		// Every start-of-frame marker but the four that mean something else
		// carries the dimensions in the same place.
		isSOF := marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			height = uint16(data[i+5])<<8 | uint16(data[i+6])
			width = uint16(data[i+7])<<8 | uint16(data[i+8])
			return width, height, width > 0 && height > 0
		}
		if marker == 0xDA || marker == 0xD9 {
			return 0, 0, false // into the scan, too late
		}
		if size < 2 {
			return 0, 0, false
		}
		i += 2 + size
	}
	return 0, 0, false
}
