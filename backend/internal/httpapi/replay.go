package httpapi

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/avi"
	"argus-nvr/internal/camera"
)

// A browser cannot play these recordings. They are MJPEG inside AVI, which
// nothing decodes natively, so a video element pointed at the download URL
// shows nothing, and transcoding on demand is a service that falls over the
// first time two people scrub at once.
//
// The camera solved this already: its playback page replays a recording as
// multipart/x-mixed-replace, the same format the live view uses, so an ordinary
// img element plays it with no decoding in the page. This does the same for
// what the service holds.

// maxReplayFrameBytes bounds the buffer one viewer is given. These cameras top
// out around a hundred kilobytes a frame at the largest size they offer, so
// anything past this is a file claiming something no camera wrote.
const maxReplayFrameBytes = 4 << 20

// maxSpeed bounds the pace a replay can be asked for. The document offers 0.5,
// 1, 2 and 4; anything past four is faster than a person can watch and is
// better served by speed 0, which sends as fast as the file reads.
const maxSpeed = 4

// replay is a recording opened for reading, with its frame index and what the
// sidecar says about it. The file stays open and the frames stay on disk: a
// recording runs to twelve megabytes and several people may be watching.
type replay struct {
	file *os.File
	idx  avi.Index
	meta archive.Meta
}

func (r *replay) close() {
	if r.file != nil {
		r.file.Close()
	}
}

// recordingID reads the identity out of the path, accepting either extension
// so a URL can gain a suffix and keep working, and so a link written down while
// a recording was an AVI still finds it after it has been transcoded.
func recordingID(r *http.Request) archive.ID {
	at := r.PathValue("at")
	for _, format := range []string{archive.FormatAVI, archive.FormatMP4} {
		at = strings.TrimSuffix(at, "."+format)
	}
	return archive.ID{
		CameraID: r.PathValue("cameraId"),
		Day:      r.PathValue("day"),
		At:       at,
	}
}

// openReplay opens a held recording and reads its index, which is a few
// kilobytes at the end of the file rather than the file itself.
func (s *Server) openReplay(w http.ResponseWriter, r *http.Request) (*replay, bool) {
	if s.archive == nil {
		writeError(w, http.StatusNotFound, "this service is not holding recordings")
		return nil, false
	}
	id := recordingID(r)

	f, info, format, err := s.archive.Open(id)
	if errors.Is(err, archive.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such recording")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	// A transcoded recording has no business here. These two routes exist only
	// because nothing decodes MJPEG in AVI in a browser, and an MP4 is played
	// by a video element from the download URL, with seeking the browser
	// provides rather than a scrubber built out of frame times.
	if format != archive.FormatAVI {
		f.Close()
		writeError(w, http.StatusUnsupportedMediaType,
			"this recording is held as MP4: play it from the recording URL with a video element")
		return nil, false
	}

	idx, err := avi.ReadIndex(f, info.Size())
	if err != nil {
		f.Close()
		// A recording that will not index is one nothing can play, which is
		// news about that recording rather than a failed request.
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("this recording cannot be replayed: %v", err))
		return nil, false
	}
	meta, _ := s.archive.Meta(id)
	return &replay{file: f, idx: idx, meta: meta}, true
}

// recordingFrames answers the frame index, so a scrubber knows where it can
// land without downloading the whole file first.
func (s *Server) recordingFrames(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.openReplay(w, r)
	if !ok {
		return
	}
	defer rep.close()

	times := make([]int64, len(rep.idx.Frames))
	for i, f := range rep.idx.Frames {
		times[i] = f.AtMs
	}

	// The camera's own listing measured the recording against its clock and is
	// the number the recording is listed under, so it is the one reported here
	// too. A recording whose sidecar went missing still has the length the file
	// itself states.
	durMs := rep.meta.DurMs
	if durMs <= 0 {
		durMs = rep.idx.DurMs
	}

	// Revalidated for the same reason the recording itself is: a recording that
	// has been transcoded since this index was cached has no frame index at
	// all, and a client holding one would replay a recording the service will
	// no longer replay.
	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"frames": len(rep.idx.Frames),
		"durMs":  durMs,
		"width":  rep.idx.Width,
		"height": rep.idx.Height,
		"times":  times,
	})
}

// recordingStream replays a held recording as multipart/x-mixed-replace, paced
// from the times in the file so a recording that ran slowly replays slowly
// rather than being rushed through at a guessed rate.
//
// Frames are read one at a time into a single buffer. Nothing here holds a
// recording in memory, because they run to twelve megabytes and the point of
// this route is that several people can watch at once.
func (s *Server) recordingStream(w http.ResponseWriter, r *http.Request) {
	speed, err := replaySpeed(r.URL.Query().Get("speed"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, ok := s.openReplay(w, r)
	if !ok {
		return
	}
	defer rep.close()

	frames := rep.idx.Frames
	// A scrubber that has run to the end asks for the frame after the last one,
	// so the end of the recording is a frame rather than an error.
	from := atoiDefault(r.URL.Query().Get("from"), 0)
	if from >= len(frames) {
		from = len(frames) - 1
	}

	size := rep.idx.MaxFrameLen
	if size > maxReplayFrameBytes {
		writeError(w, http.StatusUnprocessableEntity, "this recording holds a frame larger than anything a camera writes")
		return
	}
	buf := make([]byte, size)

	h := w.Header()
	h.Set("Content-Type", "multipart/x-mixed-replace; boundary="+camera.Boundary)
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	rc.Flush()

	ctx := r.Context()
	// The clock starts at the first frame sent rather than at the start of the
	// recording, so seeking into the middle does not spend the skipped time
	// waiting before anything appears.
	started := time.Now()
	base := frames[from].AtMs

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for i := from; i < len(frames); i++ {
		if speed > 0 {
			due := time.Duration(float64(frames[i].AtMs-base) * float64(time.Millisecond) / speed)
			if wait := due - time.Since(started); wait > 0 {
				timer.Reset(wait)
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		frame := buf[:frames[i].Length]
		if _, err := rep.file.ReadAt(frame, frames[i].Offset); err != nil {
			// The file is shorter than its index says. Whatever was sent is
			// still a replay; the connection ending is how it says it stopped.
			return
		}
		part := fmt.Sprintf("--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", camera.Boundary, len(frame))
		if _, err := w.Write([]byte(part)); err != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return
		}
		// Without an explicit flush the frames sit in Go's buffer and the
		// viewer sees nothing until it fills.
		if err := rc.Flush(); err != nil {
			return
		}
	}
	// The connection ends when the recording does, and says so with the closing
	// delimiter rather than by hanging up mid-part. A client that wants the
	// recording again asks again.
	w.Write([]byte("--" + camera.Boundary + "--\r\n"))
	rc.Flush()
}

// replaySpeed reads the pace asked for. Zero means as fast as the file reads,
// which is what a client that is buffering rather than watching wants.
func replaySpeed(s string) (float64, error) {
	if s == "" {
		return 1, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > maxSpeed {
		return 0, fmt.Errorf("speed must be 0, 0.5, 1, 2 or 4")
	}
	return v, nil
}
