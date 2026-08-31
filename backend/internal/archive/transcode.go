package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"argus-nvr/internal/avi"
)

// Transcoding is the archive's side of turning a recording into a tenth of its
// size: which file is replaced by which, in what order, and what has to be true
// before the original is let go. The encoding itself is somebody else's problem
// and arrives as an Encoder.
//
// The order is the whole of the safety here. The MP4 is written under a temp
// name, decoded back to a frame count, and only then renamed into place; the
// AVI is removed only after that rename has succeeded. A crash at any point
// leaves either the recording that arrived or the one that was verified, never
// a shorter file under a name that promises a whole recording.

// ErrAlreadyTranscoded is a recording that is already held as MP4, which is not
// a failure: it is the normal answer for a backfill that has been round twice,
// or a queue that was handed the same recording by two paths.
var ErrAlreadyTranscoded = errors.New("recording is already transcoded")

// Encoder is what an archive needs from ffmpeg, and nothing more, so that this
// package neither builds command lines nor knows what a codec is.
type Encoder interface {
	// Encode writes an H.264 MP4 of the recording at src to dst.
	Encode(ctx context.Context, src, dst string) error
	// Frames decodes a recording and reports how many frames came out of it.
	// It is the check that an encode is whole: a file that is truncated, or
	// that holds a shorter recording than the one it replaces, does not survive
	// being decoded back to the same count.
	Frames(ctx context.Context, path string) (int, error)
}

// Transcoded is what one transcode cost and saved.
type Transcoded struct {
	ID     ID
	Before int64
	After  int64
	Frames int
	Took   time.Duration
}

// Ratio is how many times smaller the recording became.
func (t Transcoded) Ratio() float64 {
	if t.After <= 0 {
		return 0
	}
	return float64(t.Before) / float64(t.After)
}

// Transcode re-encodes one held recording from MJPEG in AVI to H.264 in MP4,
// and removes the AVI once the MP4 is verified to hold the same frames.
//
// The frame count that has to be matched is read out of the AVI's own index
// rather than from the sidecar, because the sidecar carries what a camera said
// and this has to compare the file against the file. A recording whose index
// cannot be read is left exactly as it is: something nothing can index is not
// something to replace with a re-encoding of it.
func (s *Store) Transcode(ctx context.Context, id ID, enc Encoder) (Transcoded, error) {
	if !id.valid() {
		return Transcoded{}, ErrNotFound
	}
	src := s.File(id, FormatAVI)
	dst := s.File(id, FormatMP4)

	srcInfo, srcErr := os.Stat(src)
	if _, err := os.Stat(dst); err == nil {
		// Both forms on the disk is a transcode that was interrupted after the
		// MP4 was renamed in. The MP4 was verified before it got that name, so
		// finishing the job is removing the AVI, not encoding again.
		if srcErr == nil {
			os.Remove(src)
		}
		return Transcoded{}, ErrAlreadyTranscoded
	}
	if srcErr != nil {
		return Transcoded{}, ErrNotFound
	}

	want, err := aviFrames(src)
	if err != nil {
		return Transcoded{}, err
	}

	tmp, err := os.CreateTemp(s.dayDir(id.CameraID, id.Day), tempPrefix+id.At+"-mp4-*")
	if err != nil {
		return Transcoded{}, err
	}
	// The encoder writes this file itself, so what is wanted from here is the
	// name and the promise that nothing else took it.
	tmp.Close()
	defer os.Remove(tmp.Name())

	started := time.Now()
	if err := enc.Encode(ctx, src, tmp.Name()); err != nil {
		return Transcoded{}, err
	}

	got, err := enc.Frames(ctx, tmp.Name())
	if err != nil {
		return Transcoded{}, fmt.Errorf("could not read back the encode, so %s is kept as AVI: %w", id, err)
	}
	if got != want {
		return Transcoded{}, fmt.Errorf("the encode of %s holds %d frames against the AVI's %d, so the AVI is kept", id, got, want)
	}
	info, err := os.Stat(tmp.Name())
	if err != nil {
		return Transcoded{}, err
	}
	if info.Size() <= 0 {
		return Transcoded{}, fmt.Errorf("the encode of %s is empty, so the AVI is kept", id)
	}

	// An encode that came out no smaller is a worse recording than the one
	// already held: the same footage, generationally re-compressed, in a
	// container that has to be decoded to seek. The point of this is disk, so
	// when it does not save any there is nothing to weigh against the loss.
	//
	// It happens on very short or very noisy clips, where H.264 has almost no
	// redundancy to remove and pays its container overhead anyway.
	if before, err := os.Stat(src); err == nil && info.Size() >= before.Size() {
		return Transcoded{}, fmt.Errorf(
			"the encode of %s is %d bytes against the AVI's %d, so the AVI is kept",
			id, info.Size(), before.Size())
	}

	// Retention runs on its own timer and may have aged this recording out
	// while it was being encoded. Renaming the MP4 in now would bring back
	// footage the archive has already decided to let go, and the day's note
	// would say it is gone while the file sat there.
	if _, err := os.Stat(src); err != nil {
		return Transcoded{}, ErrNotFound
	}

	if err := os.Rename(tmp.Name(), dst); err != nil {
		return Transcoded{}, err
	}
	// Only now, and only ever now.
	if err := os.Remove(src); err != nil {
		return Transcoded{}, fmt.Errorf("transcoded %s but could not remove the AVI: %w", id, err)
	}

	return Transcoded{
		ID:     id,
		Before: srcInfo.Size(),
		After:  info.Size(),
		Frames: got,
		Took:   time.Since(started),
	}, nil
}

// aviFrames counts what the recording actually holds, from its own index.
func aviFrames(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	idx, err := avi.ReadIndex(f, info.Size())
	if err != nil {
		return 0, fmt.Errorf("cannot index %s, so it is left as it is: %w", path, err)
	}
	return len(idx.Frames), nil
}

// Untranscoded lists recordings still held as AVI, newest first, at most limit
// of them.
//
// Newest first because the oldest are the ones retention is about to age out,
// and spending the machine on re-encoding footage that is about to be deleted
// is the one ordering that wastes the work entirely.
func (s *Store) Untranscoded(limit int) ([]ID, error) {
	recs, err := s.scan("", "")
	if err != nil {
		return nil, err
	}
	sortNewestFirst(recs)
	out := make([]ID, 0, limit)
	for _, r := range recs {
		if r.Format != FormatAVI {
			continue
		}
		out = append(out, r.ID())
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
