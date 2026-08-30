// Package manager owns one live device per configured camera: its session, its
// stream fan-out, and the background poll that keeps its state cached.
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"esp32cam-nvr/internal/camera"
	"esp32cam-nvr/internal/store"
)

// pollInterval is deliberately slow. The cameras are microcontrollers sharing
// one radio between the poll and the video, so the docs ask for a couple of
// seconds and anything faster steals frames from viewers.
const pollInterval = 2 * time.Second

// pollTimeout is larger than pollInterval on purpose. A poll that has to
// re-login costs two round trips, and the measured round trip to a camera on
// this Wi-Fi is around three seconds, so budgeting only one interval would
// make every post-reboot poll time out and report the camera as down. Polls
// run one at a time, so a slow camera stretches its own interval rather than
// piling requests onto a device that is already struggling.
const pollTimeout = 12 * time.Second

const maxRecordBody = 64 << 10

// Status is the cached view of a camera. Record is kept as raw JSON so fields
// the firmware adds reach the UI without this service knowing about them; the
// live camera already returns several that the API docs do not list.
type Status struct {
	Online    bool            `json:"online"`
	Error     string          `json:"error,omitempty"`
	CheckedAt time.Time       `json:"checkedAt"`
	Record    json.RawMessage `json:"record,omitempty"`
	Viewers   int             `json:"viewers"`
}

// CameraView is one entry in the camera list API.
type CameraView struct {
	store.Public
	Status Status `json:"status"`
}

type device struct {
	cam    store.Camera
	client *camera.Client
	hub    *camera.Hub
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.RWMutex
	status Status

	// captureMissing records that this firmware has no /capture route, so the
	// snapshot path stops asking for it after the first 404. The live camera's
	// own UI links to /capture even though the route is not registered.
	captureMissing bool
}

// Manager keeps devices in step with the configured camera list.
type Manager struct {
	mu      sync.RWMutex
	devices map[string]*device
	closed  bool
}

// New returns an empty manager; call Sync to populate it.
func New() *Manager {
	return &Manager{devices: make(map[string]*device)}
}

// Sync starts devices for newly added cameras and stops those removed. It is
// the only place devices are created, so the poller and the store cannot drift.
func (m *Manager) Sync(cams []store.Camera) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	wanted := make(map[string]store.Camera, len(cams))
	for _, c := range cams {
		wanted[c.ID] = c
	}

	var stopped []*device
	for id, d := range m.devices {
		if _, ok := wanted[id]; !ok {
			delete(m.devices, id)
			stopped = append(stopped, d)
		}
	}
	for id, c := range wanted {
		if _, ok := m.devices[id]; ok {
			continue
		}
		m.devices[id] = m.startLocked(c)
	}
	m.mu.Unlock()

	for _, d := range stopped {
		d.stop()
	}
}

func (m *Manager) startLocked(c store.Camera) *device {
	client := camera.New(c)
	ctx, cancel := context.WithCancel(context.Background())
	d := &device{
		cam:    c,
		client: client,
		hub:    camera.NewHub(client),
		cancel: cancel,
		done:   make(chan struct{}),
		status: Status{CheckedAt: time.Now()},
	}
	go d.poll(ctx)
	return d
}

func (d *device) stop() {
	d.cancel()
	d.hub.Close()
	<-d.done
}

// Close stops every device, for graceful shutdown.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	devices := make([]*device, 0, len(m.devices))
	for _, d := range m.devices {
		devices = append(devices, d)
	}
	m.devices = make(map[string]*device)
	m.mu.Unlock()

	for _, d := range devices {
		d.stop()
	}
}

func (m *Manager) get(id string) (*device, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.devices[id]
	return d, ok
}

// Status returns the cached poll result. Browsers read this instead of each
// tab hitting each camera, which is the whole point of polling centrally.
func (m *Manager) Status(id string) (Status, bool) {
	d, ok := m.get(id)
	if !ok {
		return Status{}, false
	}
	return d.snapshotStatus(), true
}

// Views returns the camera list with live status attached, in the order given.
func (m *Manager) Views(cams []store.Camera) []CameraView {
	out := make([]CameraView, 0, len(cams))
	for _, c := range cams {
		v := CameraView{Public: c.Public()}
		if d, ok := m.get(c.ID); ok {
			v.Status = d.snapshotStatus()
		}
		out = append(out, v)
	}
	return out
}

func (d *device) snapshotStatus() Status {
	d.mu.RLock()
	s := d.status
	d.mu.RUnlock()
	s.Viewers = d.hub.Viewers()
	return s
}

// poll refreshes /record on a fixed interval. The first tick is jittered so a
// wall of cameras added at once does not thunder in lockstep forever.
func (d *device) poll(ctx context.Context) {
	defer close(d.done)

	jitter := time.Duration(rand.Int63n(int64(pollInterval)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		d.pollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *device) pollOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	body, err := d.record(ctx)
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.CheckedAt = now
	if err != nil {
		d.status.Online = false
		d.status.Error = err.Error()
		return
	}
	d.status.Online = true
	d.status.Error = ""
	d.status.Record = body
}

func (d *device) record(ctx context.Context) (json.RawMessage, error) {
	resp, err := d.client.Get(ctx, "/record")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxRecordBody))
		return nil, fmt.Errorf("/record: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordBody))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("/record: response was not JSON")
	}
	return json.RawMessage(body), nil
}

// Stream attaches a viewer to the camera's fan-out.
func (m *Manager) Stream(id string) (<-chan []byte, func(), bool) {
	d, ok := m.get(id)
	if !ok {
		return nil, nil, false
	}
	ch, cancel := d.hub.Subscribe()
	return ch, cancel, true
}

// Snapshot returns a single JPEG, preferring the firmware's /capture route and
// falling back to a frame off the stream when the route is absent.
func (m *Manager) Snapshot(ctx context.Context, id string) ([]byte, error) {
	d, ok := m.get(id)
	if !ok {
		return nil, store.ErrNotFound
	}

	d.mu.RLock()
	skipCapture := d.captureMissing
	d.mu.RUnlock()

	if !skipCapture {
		img, missing, err := d.capture(ctx)
		switch {
		case err == nil:
			return img, nil
		case missing:
			d.mu.Lock()
			d.captureMissing = true
			d.mu.Unlock()
			log.Printf("camera %s: no /capture route, serving snapshots from the stream", d.cam.Name)
		default:
			return nil, err
		}
	}
	return d.hub.Frame(ctx)
}

func (d *device) capture(ctx context.Context) (img []byte, missing bool, err error) {
	resp, err := d.client.Get(ctx, "/capture")
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, true, errors.New("/capture: not found")
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, false, fmt.Errorf("/capture: status %d", resp.StatusCode)
	}
	img, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false, err
	}
	return img, false, nil
}
