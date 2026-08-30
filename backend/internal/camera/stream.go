package camera

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"sync"
	"time"
)

// Boundary is the multipart separator the proxy re-emits to browsers. The
// value the camera uses is mirrored so a client that hardcodes it still works.
const Boundary = "espcamframeboundary"

// maxFrame bounds a single JPEG so a corrupt or hostile stream cannot exhaust
// memory. Frames measure around 36KB at 640x480, so this is generous.
const maxFrame = 4 << 20

// reconnectDelay paces reconnection while viewers are watching. Cameras reboot
// on firmware updates and settings changes and take a few seconds to come
// back, and retrying faster than they can boot only adds load.
const reconnectDelay = 2 * time.Second

// snapshotMaxAge is how stale a cached frame may be before a snapshot request
// waits for a fresh one instead.
const snapshotMaxAge = 3 * time.Second

// Hub multiplexes one upstream MJPEG connection to every viewer of a camera.
//
// This exists because the device halves its frame rate for each extra client
// streaming from it. Without the fan-out, two browser tabs on the same camera
// would cost twice the bandwidth and give everyone half the frames. It also
// means snapshots can be served from the frame already in hand rather than by
// opening a second connection.
type Hub struct {
	client *Client

	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	cancel  context.CancelFunc
	running bool
	// pumpDone closes when the current pump has fully released the upstream
	// connection. A viewer arriving while the previous pump is still tearing
	// down (a page refresh does exactly this) must not be handed to it, and
	// the replacement must not dial the camera until the old socket is gone.
	pumpDone chan struct{}
	last     []byte
	lastAt   time.Time
	lastErr  error
}

type subscriber struct {
	// Capacity one, written non-blockingly: a viewer that cannot keep up gets
	// the newest frame rather than stalling the upstream reader for everyone.
	ch     chan []byte
	closed bool
}

// NewHub builds a hub for a camera client.
func NewHub(c *Client) *Hub {
	return &Hub{client: c, subs: make(map[*subscriber]struct{})}
}

// Subscribe joins the fan-out, starting the upstream connection if this is the
// first viewer. The returned cancel function must be called by the caller;
// dropping the last subscriber closes the upstream connection.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	s := &subscriber{ch: make(chan []byte, 1)}

	h.mu.Lock()
	h.subs[s] = struct{}{}
	if !h.running {
		ctx, cancel := context.WithCancel(context.Background())
		prev := h.pumpDone
		done := make(chan struct{})
		h.cancel = cancel
		h.pumpDone = done
		h.running = true
		go h.pump(ctx, prev, done)
	}
	h.mu.Unlock()

	var once sync.Once
	return s.ch, func() { once.Do(func() { h.remove(s) }) }
}

func (h *Hub) remove(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	if len(h.subs) == 0 && h.cancel != nil {
		// Nobody is watching, so stop taking frames off the camera.
		h.cancel()
		h.cancel = nil
		// A cancelled pump can no longer serve anyone, so the next viewer must
		// start a fresh one rather than waiting on this one to deliver frames
		// it will never read.
		h.running = false
	}
}

// Close tears the hub down regardless of subscribers, for shutdown.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.running = false
	for s := range h.subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
	h.subs = make(map[*subscriber]struct{})
}

// pump owns the upstream connection for as long as anyone is watching. It
// reconnects across camera reboots so a viewer sees the stream resume instead
// of a broken image.
func (h *Hub) pump(ctx context.Context, prev <-chan struct{}, done chan struct{}) {
	defer close(done)
	defer func() {
		h.mu.Lock()
		// Only stand down if this is still the live pump; a newer one may have
		// already taken over.
		if h.pumpDone == done {
			h.running = false
		}
		h.mu.Unlock()
	}()

	if prev != nil {
		// Waiting keeps the camera at exactly one connection during a refresh
		// instead of briefly holding two.
		select {
		case <-prev:
		case <-ctx.Done():
			return
		}
	}

	for ctx.Err() == nil {
		err := h.readOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		h.setErr(err)
		if errors.Is(err, ErrUnauthorized) {
			// Wrong credentials will not fix themselves; ending the stream
			// tells the viewer something is actually misconfigured.
			h.dropAll()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

func (h *Hub) readOnce(ctx context.Context) error {
	resp, err := h.client.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("stream: upstream status %d", resp.StatusCode)
	}
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("stream: content type: %w", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return errors.New("stream: upstream sent no multipart boundary")
	}

	h.setErr(nil)
	mr := multipart.NewReader(resp.Body, boundary)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		part, err := mr.NextPart()
		if err != nil {
			return err
		}
		frame, err := io.ReadAll(io.LimitReader(part, maxFrame))
		part.Close()
		if err != nil {
			return err
		}
		if len(frame) == 0 {
			continue
		}
		h.broadcast(frame)
	}
}

func (h *Hub) broadcast(frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = frame
	h.lastAt = time.Now()
	for s := range h.subs {
		if s.closed {
			continue
		}
		select {
		case s.ch <- frame:
		default:
			// Viewer is behind: discard what it has not read and give it the
			// newest frame, so latency does not accumulate on a slow client.
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- frame:
			default:
			}
		}
	}
}

func (h *Hub) dropAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
	h.subs = make(map[*subscriber]struct{})
}

func (h *Hub) setErr(err error) {
	h.mu.Lock()
	h.lastErr = err
	h.mu.Unlock()
}

// Latest returns the most recent frame and its age.
func (h *Hub) Latest() ([]byte, time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		return nil, 0, false
	}
	return h.last, time.Since(h.lastAt), true
}

// Viewers reports how many clients are attached, which is worth surfacing
// because each camera can only serve so many.
func (h *Hub) Viewers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Frame returns a single JPEG, reusing the live stream when one is already
// running so a snapshot costs the camera nothing extra.
func (h *Hub) Frame(ctx context.Context) ([]byte, error) {
	if frame, age, ok := h.Latest(); ok && age < snapshotMaxAge {
		return frame, nil
	}

	ch, cancel := h.Subscribe()
	defer cancel()

	select {
	case frame, ok := <-ch:
		if !ok {
			h.mu.Lock()
			err := h.lastErr
			h.mu.Unlock()
			if err == nil {
				err = errors.New("stream ended before a frame arrived")
			}
			return nil, err
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
