package manager

import (
	"context"
	"log"
	"time"

	"argus-nvr/internal/archive"
)

// standInCheck is how often the decision to be recording is revisited. The
// answer comes from the poll's cached /record, so this costs the camera
// nothing; it only decides how quickly the service notices a trigger.
const standInCheck = time.Second

// standInIdle ends a recording whose frames have stopped arriving. A camera
// that rebooted mid-event is not going to say it has stopped, and a recording
// left open forever is a recording nobody can play.
const standInIdle = 10 * time.Second

// maxStandIn caps one file. A camera stuck reporting itself as recording would
// otherwise write one file until the volume is full, and a clip that runs for
// hours is unusable anyway. Passing this rolls into a new recording rather than
// stopping, so nothing is lost if the event really is that long.
const maxStandIn = 10 * time.Minute

// minStandInFrames is the shortest thing worth keeping. A recording of one or
// two frames has no measurable rate and nothing to see, and the archive is
// better off without a file for every twitch.
const minStandInFrames = 3

// recordForCamera writes the frames the service is already receiving into the
// archive, for a camera that cannot write them itself.
//
// This asks nothing of the camera. It joins the stream fan-out that the live
// view uses, so a camera being recorded for costs exactly one upstream
// connection whether nobody is watching it or six people are.
func (d *device) recordForCamera(ctx context.Context) {
	defer close(d.standInDone)

	t := time.NewTicker(standInCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if d.shouldStandIn() {
			d.standInOnce(ctx)
		}
	}
}

// shouldStandIn is the design's rule, unembellished: the camera says it has no
// usable card, and the camera says it is recording. The service records what
// the camera decided was worth recording, on the camera's own triggers, rather
// than inventing an opinion of its own about what an event is.
func (d *device) shouldStandIn() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status.Online && d.storage != "" && d.storage != "ok" && d.recording
}

func (d *device) standInOnce(ctx context.Context) {
	id, err := d.freeID(time.Now())
	if err != nil {
		log.Printf("camera %s: not recording on its behalf: %v", d.cam.Name, err)
		return
	}
	pending, err := d.arch.Create(id)
	if err != nil {
		log.Printf("camera %s: could not start recording on its behalf: %v", d.cam.Name, err)
		return
	}

	frames, unsubscribe := d.frames.Subscribe()
	defer unsubscribe()

	d.setStandIn(true)
	defer d.setStandIn(false)

	started := time.Now()
	check := time.NewTicker(standInCheck)
	defer check.Stop()
	idle := time.NewTimer(standInIdle)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			d.finish(pending)
			return

		case frame, open := <-frames:
			if !open {
				// The hub gave up on the camera, so there are no more frames
				// to write. What arrived is still footage.
				d.finish(pending)
				return
			}
			if err := pending.WriteFrame(frame, time.Now()); err != nil {
				log.Printf("camera %s: recording on its behalf failed: %v", d.cam.Name, err)
				pending.Abort()
				return
			}
			idle.Reset(standInIdle)

		case <-idle.C:
			log.Printf("camera %s: no frames for %s, ending the recording made on its behalf", d.cam.Name, standInIdle)
			d.finish(pending)
			return

		case <-check.C:
			if !d.shouldStandIn() {
				d.finish(pending)
				return
			}
			if time.Since(started) >= maxStandIn {
				// Roll over rather than stop: the camera still says it is
				// recording, and the next tick of the outer loop starts the
				// next file.
				d.finish(pending)
				return
			}
		}
	}
}

// finish keeps what is worth keeping and throws away what is not.
func (d *device) finish(p *archive.Pending) {
	if p.Frames() < minStandInFrames {
		p.Abort()
		return
	}
	rec, err := p.Commit()
	if err != nil {
		log.Printf("camera %s: could not store the recording made on its behalf: %v", d.cam.Name, err)
		return
	}
	log.Printf("camera %s: recorded on its behalf: %s, %d frames over %.1fs, %d KB",
		d.cam.Name, rec.At, rec.Frames, float64(rec.DurMs)/1000, rec.Bytes>>10)

	// Committed, named and listable before anything re-encodes it. This is the
	// only copy of this footage, so the order matters more here than anywhere.
	d.offerForTranscode(rec.ID())

	if removed, freed, err := d.arch.Sweep(); err != nil {
		log.Printf("archive: retention pass failed: %v", err)
	} else if removed > 0 {
		log.Printf("archive: aged out %d recording(s), %d MB", removed, freed>>20)
	}
}

// freeID names the recording after the moment it started, and steps a second at
// a time past anything already held. Two recordings a second apart is not a
// case that happens; a restart inside the same second is, and overwriting a
// recording that already exists is the one outcome worth ruling out.
func (d *device) freeID(at time.Time) (archive.ID, error) {
	for i := 0; i < 60; i++ {
		id := archive.ID{
			CameraID: d.archiveKey(),
			Day:      at.Format("2006-01-02"),
			At:       at.Format("150405"),
		}
		if !d.arch.Has(id) {
			return id, nil
		}
		at = at.Add(time.Second)
	}
	return archive.ID{}, errAlreadyHeld
}

type standInError string

func (e standInError) Error() string { return string(e) }

const errAlreadyHeld = standInError("a recording is already held for every second of the last minute")

func (d *device) setStandIn(on bool) {
	d.mu.Lock()
	d.status.StandIn = on
	d.mu.Unlock()
}
