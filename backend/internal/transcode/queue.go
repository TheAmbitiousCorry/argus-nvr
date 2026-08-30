package transcode

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"argus-nvr/internal/archive"
)

// The rules this queue exists to keep:
//
// One at a time. There is one worker, and every path into transcoding goes
// through the same channel, so a camera that has just been caught up with and a
// backfill of two hundred recordings cannot both be encoding at once.
//
// After the recording is safe. Nothing is queued until it has been committed to
// the archive under its real name, so a transcode that fails costs a larger
// file rather than the recording.
//
// Not all at once. The backfill hands over one recording at a time and only
// when nothing else is waiting, so a restart with a full archive is a trickle
// in the background rather than an afternoon of the machine being unusable.

// queueDepth is how many recordings can be waiting. A camera makes a recording
// every few minutes at worst and each takes about a second to encode, so this
// is far more headroom than the live path can use; a backfill deliberately does
// not use it at all.
const queueDepth = 64

// DefaultBackfillGap is the pause between one recording of an existing archive
// and the next. It is not the encode's own pace, which is bounded by there
// being one worker at low priority: it is what keeps a backfill from being the
// only thing this machine does after a restart.
const DefaultBackfillGap = 10 * time.Second

// backfillBatch is how many candidates are looked at to find one worth doing.
// More than one, because a recording that cannot be transcoded must not be the
// thing the backfill retries forever instead of making progress.
const backfillBatch = 64

// idleGap is how long to wait before looking again once everything held is
// transcoded, which on a settled archive is almost always.
const idleGap = 5 * time.Minute

// Queue holds recordings waiting to be transcoded, and the one worker that
// transcodes them.
type Queue struct {
	arch *archive.Store
	enc  archive.Encoder
	work chan archive.ID

	mu      sync.Mutex
	waiting map[archive.ID]bool
	// failed is what not to keep trying. A recording whose AVI cannot be
	// indexed, or that ffmpeg will not encode, fails the same way every time,
	// and a backfill that retries it is a backfill that never reaches the rest
	// of the archive. It is forgotten on restart, which is the right amount of
	// stubbornness: a new version of ffmpeg deserves one more go.
	failed map[archive.ID]bool

	done    int
	saved   int64
	running bool
}

// New returns a queue over an archive. Nothing is transcoded until Run.
func New(arch *archive.Store, enc archive.Encoder) *Queue {
	return &Queue{
		arch:    arch,
		enc:     enc,
		work:    make(chan archive.ID, queueDepth),
		waiting: make(map[archive.ID]bool),
		failed:  make(map[archive.ID]bool),
	}
}

// Add offers a recording for transcoding. It never blocks: this is called from
// the path that has just finished storing a recording, and that path waiting on
// an encoder would put transcoding back in front of the disk, which is the one
// thing the design forbids. A recording that does not fit is simply left for
// the backfill to find.
func (q *Queue) Add(id archive.ID) {
	q.mu.Lock()
	if q.waiting[id] || q.failed[id] {
		q.mu.Unlock()
		return
	}
	q.waiting[id] = true
	q.mu.Unlock()

	select {
	case q.work <- id:
	default:
		q.mu.Lock()
		delete(q.waiting, id)
		q.mu.Unlock()
	}
}

// Run is the one worker. It returns when ctx is cancelled, and never runs two
// encodes at once because there is only ever one of it.
func (q *Queue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q.work:
			q.mu.Lock()
			delete(q.waiting, id)
			q.running = true
			q.mu.Unlock()

			q.transcode(ctx, id)

			q.mu.Lock()
			q.running = false
			q.mu.Unlock()
		}
	}
}

func (q *Queue) transcode(ctx context.Context, id archive.ID) {
	out, err := q.arch.Transcode(ctx, id, q.enc)
	switch {
	case errors.Is(err, archive.ErrAlreadyTranscoded):
		return
	case errors.Is(err, archive.ErrNotFound):
		// Retention took it, or a camera was removed with it. Neither is worth
		// a line in the log.
		return
	case ctx.Err() != nil:
		return
	case err != nil:
		q.mu.Lock()
		q.failed[id] = true
		q.mu.Unlock()
		log.Printf("recordings: %v", err)
		return
	}

	q.mu.Lock()
	q.done++
	q.saved += out.Before - out.After
	done, saved := q.done, q.saved
	q.mu.Unlock()

	log.Printf("recordings: transcoded %s, %d KB to %d KB (%.1fx) in %s; %d done, %d MB saved",
		id, out.Before>>10, out.After>>10, out.Ratio(), out.Took.Round(time.Millisecond), done, saved>>20)
}

// Backfill works through what the archive already holds, one recording at a
// time, with a gap between them, and only when nothing that has just arrived is
// waiting. An archive of two hundred recordings from before any of this existed
// is transcoded over an hour or so of background work rather than in one burst
// on startup.
func (q *Queue) Backfill(ctx context.Context, gap time.Duration) {
	if gap <= 0 {
		gap = DefaultBackfillGap
	}
	wait := gap
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = gap

		if q.busy() {
			continue
		}
		ids, err := q.arch.Untranscoded(backfillBatch)
		if err != nil {
			log.Printf("recordings: could not look for recordings to transcode: %v", err)
			wait = idleGap
			continue
		}
		id, ok := q.next(ids)
		if !ok {
			// Everything held is either transcoded or known to fail, so there
			// is nothing to be gained by asking again soon.
			wait = idleGap
			continue
		}
		q.Add(id)
	}
}

// next picks the first candidate that has not already failed.
func (q *Queue) next(ids []archive.ID) (archive.ID, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range ids {
		if !q.failed[id] && !q.waiting[id] {
			return id, true
		}
	}
	return archive.ID{}, false
}

// busy reports whether anything is queued or being encoded, which is what the
// backfill waits for: a recording that has just been pulled off a card is more
// worth encoding now than one that has been sitting on the disk for a week.
func (q *Queue) busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running || len(q.waiting) > 0
}
