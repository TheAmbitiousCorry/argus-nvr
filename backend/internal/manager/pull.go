package manager

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"sort"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/camera"
)

// pullInterval is how often a camera is asked what it holds. Recordings are
// minutes apart at best, so this is slow enough to cost the camera nothing and
// quick enough that a clip is on the disk while it is still worth watching.
const pullInterval = 60 * time.Second

// pullRetryInterval is the slower beat for a camera whose firmware has no
// recordings listing. It is the normal state of an un-updated camera rather
// than a fault, so it is asked again occasionally in case it was updated, and
// otherwise left alone.
const pullRetryInterval = 10 * time.Minute

// listTimeout covers one listing request, which may first have to log in again.
const listTimeout = 25 * time.Second

// downloadTimeout bounds one recording. The measured cameras serve a ten second
// clip of around twelve megabytes; this leaves room for a much longer one over
// a much worse link before the download is called stuck.
const downloadTimeout = 10 * time.Minute

// betweenDownloads is the pause between one recording and the next.
//
// Downloads are already strictly sequential per camera, which is what keeps a
// catch-up after an outage from saturating the radio the live view shares. The
// gap on top of that leaves the camera a moment to answer a viewer, so a
// hundred recordings arriving is a background trickle rather than a wall.
//
// A variable rather than a constant only so a test can pull a card's worth of
// recordings without waiting out the courtesy owed to a real radio.
var betweenDownloads = 2 * time.Second

// pull keeps the archive in step with what a camera holds on its own card.
//
// It runs in its own goroutine, one per camera, and does one thing at a time:
// list the days, list each day, download what is missing. Nothing is
// remembered between passes, because the archive on disk is the record of what
// has been pulled and asking it is what makes this idempotent across restarts.
func (d *device) pull(ctx context.Context) {
	defer close(d.pullDone)

	// A fleet added at once would otherwise all list, and then all download, in
	// lockstep on the same second forever.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(rand.Int63n(int64(pullInterval)))):
	}

	for {
		wait := pullInterval
		if !d.pullOnce(ctx) {
			wait = pullRetryInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// pullOnce does one full pass and reports whether the camera has the listing at
// all, which is the only reason to come back at a different pace.
func (d *device) pullOnce(ctx context.Context) (supported bool) {
	if !d.online() {
		// A camera that is not answering its poll will not answer a listing
		// either, and asking anyway only adds requests to a device that may be
		// rebooting. There is nothing to lose by waiting: the recordings are
		// on its card, and they will still be there when it is back.
		return true
	}

	days, err := d.listDays(ctx)
	switch {
	case errors.Is(err, camera.ErrNoListing):
		d.noteNoListing()
		return false
	case err != nil:
		d.notePull(err)
		return true
	}

	// Oldest first: after an outage the oldest missing recording is the one
	// closest to being lost, since it is the one nearest the camera's own
	// housekeeping deleting it to keep the card free.
	sort.Strings(days)

	pulled := 0
	for _, day := range days {
		n, err := d.pullDay(ctx, day)
		pulled += n
		if err != nil {
			if ctx.Err() != nil {
				return true
			}
			d.notePull(err)
			return true
		}
	}

	d.notePull(nil)
	if pulled > 0 {
		log.Printf("camera %s: pulled %d recording(s) from its card", d.cam.Name, pulled)
		if removed, freed, err := d.arch.Sweep(); err != nil {
			log.Printf("archive: retention pass failed: %v", err)
		} else if removed > 0 {
			log.Printf("archive: aged out %d recording(s), %d MB", removed, freed>>20)
		}
	}
	return true
}

func (d *device) listDays(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	return d.client.Days(ctx)
}

// pullDay downloads everything in one day that the archive does not already
// hold, one recording at a time.
func (d *device) pullDay(ctx context.Context, day string) (int, error) {
	held, err := d.arch.Held(d.cam.ID, day)
	if err != nil {
		return 0, err
	}

	pulled := 0
	for start := 0; ; {
		listCtx, cancel := context.WithTimeout(ctx, listTimeout)
		recs, more, err := d.client.Recordings(listCtx, day, start)
		cancel()
		if err != nil {
			return pulled, err
		}
		if len(recs) == 0 {
			return pulled, nil
		}

		for _, r := range recs {
			if ctx.Err() != nil {
				return pulled, ctx.Err()
			}
			if held[r.At] || !archive.ValidAt(r.At) {
				continue
			}
			if err := d.download(ctx, day, r); err != nil {
				if ctx.Err() != nil {
					return pulled, ctx.Err()
				}
				// One recording that will not come across is not a reason to
				// abandon the rest of the day. It is reported and skipped, and
				// the next pass will try it again.
				log.Printf("camera %s: could not pull %s/%s: %v", d.cam.Name, day, r.At, err)
				continue
			}
			pulled++
			select {
			case <-ctx.Done():
				return pulled, ctx.Err()
			case <-time.After(betweenDownloads):
			}
		}

		if !more {
			return pulled, nil
		}
		start += len(recs)
	}
}

func (d *device) download(ctx context.Context, day string, r camera.Recording) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	d.setFetching(true)
	defer d.setFetching(false)

	resp, err := d.client.OpenRecording(ctx, day, r.At)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	id := archive.ID{CameraID: d.cam.ID, Day: day, At: r.At}
	bytes, err := d.arch.Save(id, resp.Body, archive.Meta{
		DurMs:  r.DurMs,
		Frames: r.Frames,
		Source: archive.SourceCamera,
	})
	if err != nil {
		return err
	}
	log.Printf("camera %s: held %s/%s, %d KB", d.cam.Name, day, r.At, bytes>>10)
	return nil
}

// setFetching tells the poller to stand aside, and to start again afterwards.
func (d *device) setFetching(on bool) {
	d.mu.Lock()
	d.fetching = on
	d.mu.Unlock()
}

func (d *device) online() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status.Online
}

func (d *device) notePull(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	d.status.PulledAt = &now
	if err != nil {
		d.status.PullError = err.Error()
		return
	}
	d.status.PullError = ""
}

// noteNoListing says so once rather than every ten minutes. A camera running
// firmware from before the listing existed is a thing an operator should be
// told, and then not told again.
func (d *device) noteNoListing() {
	d.mu.Lock()
	first := !d.listingMissing
	d.listingMissing = true
	now := time.Now()
	d.status.PulledAt = &now
	d.status.PullError = camera.ErrNoListing.Error()
	d.mu.Unlock()

	if first {
		log.Printf("camera %s: firmware has no recordings listing, so nothing can be pulled from its card", d.cam.Name)
	}
}
