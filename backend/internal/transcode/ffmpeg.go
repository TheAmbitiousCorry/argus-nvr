// Package transcode re-encodes held recordings from the MJPEG in AVI the
// cameras write to H.264 in MP4.
//
// MJPEG stores every frame whole, knowing nothing of the frame before it. On a
// scene that mostly does not move, which is what a security camera watches,
// almost all of that is waste. The second gain is not about size: a browser
// decodes H.264 natively, so a transcoded recording plays in a video element
// with seeking and a timeline the browser provides, rather than through the
// frame-by-frame replay that exists only because nothing decodes MJPEG in AVI.
//
// Nothing in Go encodes H.264 well, so this shells out to ffmpeg. That makes
// ffmpeg a dependency, and it must not be a requirement: a service that will
// not start without it has traded a feature about saving disk for a service
// that does not run. Missing ffmpeg is said once and nothing is transcoded.
package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoFFmpeg is a machine with no ffmpeg on it. It is not a failure to start.
var ErrNoFFmpeg = errors.New("transcode: no ffmpeg on this machine, so recordings stay as AVI")

// DefaultCRF is the quality H.264 is asked for. Lower is better and larger;
// 24 is a compromise measured on this footage, and someone watching a driveway
// may want 20 where someone watching a corridor wants 28.
const DefaultCRF = 24

// crfRange is what x264 accepts. Anything outside it is a misconfiguration
// worth refusing rather than passing to ffmpeg to reject.
const (
	minCRF = 0
	maxCRF = 51
)

// preset is x264's speed against compression. It is deliberately not a setting.
//
// Measured over twenty real recordings from these cameras, 73.6MB of MJPEG:
// veryfast produced 34.6MB and medium 38.7MB. The slower presets are both
// slower and larger here, because CRF holds quality constant and the sensor
// noise these cameras produce is exactly what the slower presets spend their
// extra effort preserving. There is nothing to offer an operator in a knob
// whose every other position is worse.
const preset = "veryfast"

// threads keeps one encode to one core. The machine's other job is relaying
// live video from several cameras, and a transcode that takes every core for a
// second is felt by everyone watching.
const threads = "1"

// FFmpeg encodes through the ffmpeg binary found on the machine.
type FFmpeg struct {
	bin string
	crf int
}

// Find locates ffmpeg. bin may name a binary or a path; empty means look for
// "ffmpeg" on PATH. A machine without it gets ErrNoFFmpeg, which is a thing to
// log rather than a thing to stop for.
func Find(bin string, crf int) (*FFmpeg, error) {
	if bin == "" {
		bin = "ffmpeg"
	}
	if crf < minCRF || crf > maxCRF {
		return nil, fmt.Errorf("transcode: crf must be between %d and %d, not %d", minCRF, maxCRF, crf)
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoFFmpeg, err)
	}
	return &FFmpeg{bin: path, crf: crf}, nil
}

// Bin is the binary being used, for logging.
func (f *FFmpeg) Bin() string { return f.bin }

// CRF is the quality being asked for, for logging.
func (f *FFmpeg) CRF() int { return f.crf }

// Encode writes an H.264 MP4 of the AVI at src to dst.
//
// The output format is stated rather than guessed from the name, because dst is
// a temp file: the archive writes every recording under a name nothing can read
// and renames it into place only once it is whole.
//
// -fps_mode passthrough is the one flag that has to be right. These recordings
// run at whatever rate the radio left room for, and ffmpeg's default is to
// force a constant rate by duplicating and dropping frames. That would put a
// different number of frames in the MP4 than the AVI holds, which is both a
// worse recording and a failure of the check that lets the AVI be deleted.
func (f *FFmpeg) Encode(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, f.bin,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-map", "0:v:0",
		"-an",
		"-c:v", "libx264",
		"-preset", preset,
		"-crf", strconv.Itoa(f.crf),
		"-pix_fmt", "yuv420p",
		"-fps_mode", "passthrough",
		// The moov atom goes at the front, so a browser can start playing
		// without first fetching to the end of the file.
		"-movflags", "+faststart",
		"-threads", threads,
		"-f", "mp4",
		dst,
	)
	prepare(cmd)

	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, left: maxErrorBytes}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	lower(cmd)
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg: %v: %s", err, lastLine(msg))
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

// Frames decodes a recording and reports how many frames came out.
//
// This is a full decode, not a look at the header, which is the point: a file
// whose header claims a recording it does not hold, or that was cut short
// between being written and being checked, does not decode to the frame count
// it is compared against. ffprobe would answer faster from the sample table,
// but the sample table is exactly what a truncated file lies about, and leaving
// ffprobe out of the image saves shipping a second seventy megabyte binary.
func (f *FFmpeg) Frames(ctx context.Context, path string) (int, error) {
	cmd := exec.CommandContext(ctx, f.bin,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		// Progress goes to stdout as key=value lines, which is a stable
		// interface where the human-readable stats line is not.
		"-progress", "pipe:1",
		"-i", path,
		"-map", "0:v:0",
		"-threads", threads,
		"-f", "null", "-",
	)
	prepare(cmd)

	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, left: maxErrorBytes}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	lower(cmd)
	frames, scanErr := lastFrameCount(out)
	// The pipe is drained above; whatever is left is discarded so a decode that
	// outruns the reader cannot block on a full pipe.
	io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return 0, fmt.Errorf("ffmpeg: %v: %s", err, lastLine(msg))
		}
		return 0, fmt.Errorf("ffmpeg: %w", err)
	}
	if scanErr != nil {
		return 0, scanErr
	}
	// A decode that ran to the end says so. Without it the count is of however
	// far it got, which is the one number that must not be trusted here.
	if frames < 0 {
		return 0, errors.New("ffmpeg: the decode did not finish, so the frame count is unknown")
	}
	// ffmpeg reports a frame count but not every failure to decode a frame, so
	// an error on stderr from a run that still exited zero is treated as one.
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return 0, fmt.Errorf("ffmpeg: %s", lastLine(msg))
	}
	return frames, nil
}

// lastFrameCount reads ffmpeg's progress stream and returns the frame count at
// the point it reported it had finished, or -1 if it never did.
func lastFrameCount(r io.Reader) (int, error) {
	frames := 0
	done := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch key {
		case "frame":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				frames = n
			}
		case "progress":
			done = strings.TrimSpace(value) == "end"
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if !done {
		return -1, nil
	}
	return frames, nil
}

// maxErrorBytes bounds what is kept of a failing ffmpeg's complaints. A run
// that fails on every frame of a long recording can write a great deal.
const maxErrorBytes = 8 << 10

type limitedWriter struct {
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.left <= 0 {
		return n, nil
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	written, err := l.w.Write(p)
	l.left -= written
	if err != nil {
		return written, err
	}
	return n, nil
}

// lastLine is what to put in a log line. ffmpeg's last word on a file is the
// one that says what was wrong with it.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return s
}
