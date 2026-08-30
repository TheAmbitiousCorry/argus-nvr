package transcode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/avi"
)

// countingEncoder is a stand-in for ffmpeg that records how many encodes are
// running at once, which is the rule the queue exists to keep.
type countingEncoder struct {
	hold time.Duration
	// fail names a start time that will not encode, however often it is tried.
	fail string

	mu        sync.Mutex
	running   int
	maxAtOnce int
	attempts  map[string]int
}

func newCountingEncoder() *countingEncoder {
	return &countingEncoder{attempts: make(map[string]int)}
}

func (c *countingEncoder) Encode(ctx context.Context, src, dst string) error {
	c.mu.Lock()
	c.running++
	if c.running > c.maxAtOnce {
		c.maxAtOnce = c.running
	}
	at := filepath.Base(src)
	c.attempts[at]++
	fail := c.fail != "" && at == c.fail+".avi"
	c.mu.Unlock()

	if c.hold > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(c.hold):
		}
	}

	c.mu.Lock()
	c.running--
	c.mu.Unlock()

	if fail {
		return errors.New("ffmpeg: this one never works")
	}
	// A stand-in output whose "frame count" is whatever Frames reports.
	return os.WriteFile(dst, []byte("encoded"), 0o600)
}

func (c *countingEncoder) Frames(ctx context.Context, path string) (int, error) {
	// Every recording in these tests holds the same number of frames, so this
	// matches by construction and the archive's check passes.
	return queueTestFrames, nil
}

const queueTestFrames = 6

func (c *countingEncoder) busiest() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxAtOnce
}

func (c *countingEncoder) triedFor(at string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[at+".avi"]
}

func archiveWith(t *testing.T, ats ...string) (*archive.Store, []archive.ID) {
	t.Helper()
	arch, err := archive.Open(filepath.Join(t.TempDir(), "recordings"), 0)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	clip := aviBytes(t, queueTestFrames)
	ids := make([]archive.ID, 0, len(ats))
	for _, at := range ats {
		id := archive.ID{CameraID: "a1b2", Day: "2026-08-30", At: at}
		if _, err := arch.Save(id, bytes.NewReader(clip), archive.Meta{Source: archive.SourceCamera}); err != nil {
			t.Fatalf("save %s: %v", at, err)
		}
		ids = append(ids, id)
	}
	return arch, ids
}

// aviBytes is a real AVI of n frames, without the cost of encoding real
// pictures: what is being tested here is the queue, not the codec.
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

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestQueueTranscodesOneAtATime(t *testing.T) {
	arch, ids := archiveWith(t, "090000", "100000", "110000", "120000")
	enc := newCountingEncoder()
	enc.hold = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(arch, enc)
	go q.Run(ctx)
	for _, id := range ids {
		q.Add(id)
	}

	eventually(t, "every recording to be transcoded", func() bool {
		left, err := arch.Untranscoded(10)
		return err == nil && len(left) == 0
	})
	if got := enc.busiest(); got != 1 {
		t.Errorf("%d encodes ran at once, the rule is one", got)
	}
}

func TestQueueDoesNotBlockTheCallerWhenItIsFull(t *testing.T) {
	arch, _ := archiveWith(t)
	q := New(arch, newCountingEncoder())
	// Nothing is running, so nothing is draining the queue.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < queueDepth*3; i++ {
			q.Add(archive.ID{CameraID: "a1b2", Day: "2026-08-30", At: "09" + string(rune('0'+i%10)) + "000"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Add blocked on a full queue, which would hold up storing a recording")
	}
}

func TestBackfillWorksThroughWhatIsAlreadyHeld(t *testing.T) {
	arch, _ := archiveWith(t, "090000", "100000", "110000")
	enc := newCountingEncoder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(arch, enc)
	go q.Run(ctx)
	go q.Backfill(ctx, 5*time.Millisecond)

	eventually(t, "the backfill to work through the archive", func() bool {
		left, err := arch.Untranscoded(10)
		return err == nil && len(left) == 0
	})
	if got := enc.busiest(); got != 1 {
		t.Errorf("the backfill ran %d encodes at once", got)
	}
}

func TestBackfillGivesUpOnARecordingThatWillNotEncode(t *testing.T) {
	arch, _ := archiveWith(t, "090000", "100000")
	enc := newCountingEncoder()
	// The newest is the one the backfill reaches first, and the one that fails.
	enc.fail = "100000"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(arch, enc)
	go q.Run(ctx)
	go q.Backfill(ctx, 5*time.Millisecond)

	// The one that can be transcoded is, rather than the backfill spending
	// forever on the one that cannot.
	eventually(t, "the rest of the archive to be transcoded", func() bool {
		left, err := arch.Untranscoded(10)
		return err == nil && len(left) == 1 && left[0].At == "100000"
	})
	time.Sleep(100 * time.Millisecond)
	if tries := enc.triedFor("100000"); tries != 1 {
		t.Errorf("the failing recording was tried %d times", tries)
	}
}
