package manager

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"argus-nvr/internal/camera"
	"argus-nvr/internal/store"
)

// settingsTimeout covers a read of /config and the form posts that follow it.
// Each is a round trip to a microcontroller that may first have to log us in
// again, and the measured round trip is around three seconds.
const settingsTimeout = 45 * time.Second

// flashTimeout is deliberately generous. An upload takes about seven seconds on
// this network, but a camera on a weak signal is still making progress long
// after that and cutting it off mid-write is how a device ends up unbootable.
const flashTimeout = 5 * time.Minute

// Result is one camera's outcome in a bulk operation. Bytes is only meaningful
// for a firmware flash and is left out elsewhere.
type Result struct {
	CameraID string `json:"cameraId"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
}

// Config returns one camera's whole /config document, unparsed.
func (m *Manager) Config(ctx context.Context, id string) (json.RawMessage, error) {
	d, ok := m.get(id)
	if !ok {
		return nil, store.ErrNotFound
	}
	return d.client.Config(ctx)
}

// ApplySettings applies the same partial update to every named camera. Cameras
// are independent devices, so one that is unplugged or asleep must not decide
// the outcome for the rest: every camera gets a result and no failure is
// allowed to end the run. Results come back in the order the ids were given.
func (m *Manager) ApplySettings(ctx context.Context, ids []string, s camera.Settings) []Result {
	results := make([]Result, len(ids))
	var wg sync.WaitGroup

	for i, id := range ids {
		d, ok := m.get(id)
		if !ok {
			results[i] = Result{CameraID: id, Error: "no such camera"}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
			defer cancel()
			if err := d.client.ApplySettings(ctx, s); err != nil {
				results[i] = Result{CameraID: id, Error: err.Error()}
				return
			}
			results[i] = Result{CameraID: id, OK: true}
		}()
	}
	wg.Wait()
	return results
}

// Flash writes the same firmware image to every named camera, strictly one at a
// time. Each camera reboots as it finishes, and a failure part way through
// stops the run: the cameras after it are reported untouched rather than left
// in an unknown state while the operator is still reading the first error.
func (m *Manager) Flash(ctx context.Context, ids []string, image []byte) []Result {
	results := make([]Result, 0, len(ids))
	stopped := false

	for _, id := range ids {
		if stopped {
			results = append(results, Result{CameraID: id, Error: "not attempted: an earlier camera failed"})
			continue
		}
		d, ok := m.get(id)
		if !ok {
			// Nothing was written, so the fleet is still in a known state and
			// the remaining cameras can go ahead.
			results = append(results, Result{CameraID: id, Error: "no such camera"})
			continue
		}

		err := func() error {
			ctx, cancel := context.WithTimeout(ctx, flashTimeout)
			defer cancel()
			return d.client.Flash(ctx, image)
		}()
		if err != nil {
			log.Printf("camera %s: firmware update failed: %v", d.cam.Name, err)
			results = append(results, Result{CameraID: id, Error: err.Error()})
			stopped = true
			continue
		}
		log.Printf("camera %s: firmware updated, %d bytes", d.cam.Name, len(image))
		results = append(results, Result{CameraID: id, OK: true, Bytes: len(image)})
	}
	return results
}
