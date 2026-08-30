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

	"argus-nvr/internal/archive"
	"argus-nvr/internal/camera"
	"argus-nvr/internal/store"
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

	// StandIn is true while the service is writing this camera's stream to the
	// archive because the camera cannot write it to a card of its own.
	StandIn bool `json:"standIn"`
	// Fetching is true while a recording is being downloaded off this camera's
	// card. The poll stands aside for the length of a download, so this is also
	// why checkedAt stops moving during a catch-up.
	Fetching bool `json:"fetching"`
	// PulledAt and PullError are the last attempt to catch up with what is on
	// the camera's card. A camera whose recordings are not arriving is worth
	// seeing on the same screen as one that is offline. PulledAt is a pointer
	// so that a camera nothing has been attempted for yet says nothing, rather
	// than reporting the zero time as though it were a real answer.
	PulledAt  *time.Time `json:"pulledAt,omitempty"`
	PullError string     `json:"pullError,omitempty"`
}

// CameraView is one entry in the camera list API.
type CameraView struct {
	store.Public
	Status Status `json:"status"`
}

// frameSource is where a recording made on a camera's behalf gets its frames.
// It is the stream fan-out in every real case; naming it lets a test hand the
// recorder frames without a camera on the other end of a socket.
type frameSource interface {
	Subscribe() (<-chan []byte, func())
}

type device struct {
	cam    store.Camera
	client *camera.Client
	hub    *camera.Hub
	frames frameSource
	arch   *archive.Store
	cancel context.CancelFunc
	done   chan struct{}
	// pullDone and standInDone are nil when there is no archive to write to,
	// which is what running without a data volume looks like.
	pullDone    chan struct{}
	standInDone chan struct{}

	mu     sync.RWMutex
	status Status
	// recording and storage are the two fields of /record that decide whether
	// the service records on this camera's behalf, lifted out of the raw
	// document so the decision does not re-parse it every second.
	recording bool
	storage   string
	// listingMissing remembers that this firmware answered 404 to the
	// recordings listing, so it is said once rather than on every attempt.
	listingMissing bool
	// fetching is set while a recording is being downloaded off this camera's
	// card, which is when the poll stands aside.
	fetching bool

	// captureMissing records that this firmware has no /capture route, so the
	// snapshot path stops asking for it after the first 404. The live camera's
	// own UI links to /capture even though the route is not registered.
	captureMissing bool
}

// Manager keeps devices in step with the configured camera list.
type Manager struct {
	arch *archive.Store

	mu      sync.RWMutex
	devices map[string]*device
	closed  bool
}

// New returns an empty manager; call Sync to populate it. arch may be nil, in
// which case cameras are watched and proxied but nothing is pulled off their
// cards and nothing is recorded on their behalf.
func New(arch *archive.Store) *Manager {
	return &Manager{arch: arch, devices: make(map[string]*device)}
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
		// A camera whose address or credentials changed is a different device
		// as far as the session and the poller are concerned, so it is torn
		// down and started again rather than left talking to the old address.
		if want, ok := wanted[id]; !ok || want != d.cam {
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
	hub := camera.NewHub(client)
	ctx, cancel := context.WithCancel(context.Background())
	d := &device{
		cam:    c,
		client: client,
		hub:    hub,
		frames: hub,
		arch:   m.arch,
		cancel: cancel,
		done:   make(chan struct{}),
		status: Status{CheckedAt: time.Now()},
	}
	go d.poll(ctx)
	if d.arch != nil {
		d.pullDone = make(chan struct{})
		d.standInDone = make(chan struct{})
		go d.pull(ctx)
		go d.recordForCamera(ctx)
	}
	return d
}

// stop ends every goroutine this device owns and waits for them. Waiting is
// what makes a camera being removed, or its address being corrected, safe while
// a recording is being written for it: the recording is finished or thrown away
// before the device is let go.
func (d *device) stop() {
	d.cancel()
	d.hub.Close()
	<-d.done
	for _, done := range []chan struct{}{d.pullDone, d.standInDone} {
		if done != nil {
			<-done
		}
	}
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
	s.Fetching = d.fetching
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
	// A camera has one radio, and a download is already using it. Polling
	// through a catch-up costs the download throughput and times out often
	// enough to report a camera that is busy answering us as offline, which is
	// the one thing the status is there to get right.
	d.mu.RLock()
	busy := d.fetching
	d.mu.RUnlock()
	if busy {
		return
	}

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

	var state struct {
		Active  bool   `json:"active"`
		Storage string `json:"storage"`
	}
	// A firmware from before the storage flag says nothing, which reads as an
	// empty string and means the same as "do not record for me": a camera that
	// cannot say whether it has a card is not one to start writing files for.
	if err := json.Unmarshal(body, &state); err == nil {
		d.recording = state.Active
		d.storage = state.Storage
	}
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
