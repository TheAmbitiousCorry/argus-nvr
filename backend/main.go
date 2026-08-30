// Command argus-nvr aggregates several ESP32-CAM cameras behind one HTTP
// service: it holds their sessions, proxies their video, and polls their state
// so browsers never talk to the microcontrollers directly.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"argus-nvr/internal/archive"
	"argus-nvr/internal/discovery"
	"argus-nvr/internal/httpapi"
	"argus-nvr/internal/manager"
	"argus-nvr/internal/store"
)

// addressCheckEvery is how often stored addresses are checked against what
// discovery can see. Cameras move rarely, so this is slow on purpose.
const addressCheckEvery = 30 * time.Second

// retentionEvery is how often the archive is aged down to its size limit. A
// sweep is also run after anything is written, so this is the backstop for a
// volume that filled from somewhere else rather than the main path.
const retentionEvery = 5 * time.Minute

// defaultArchiveMax is what the archive is held to when nobody says otherwise:
// enough for weeks of a few cameras' events, and small enough to leave room on
// the kind of volume this runs on.
const defaultArchiveMax = 20 << 30

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	dataPath := flag.String("data", "./data/cameras.json", "path to the camera list JSON file")
	staticDir := flag.String("static", "./web", "directory of built frontend files, empty to disable")
	recDir := flag.String("recordings", "./data/recordings", "directory recordings are held in, empty to hold none")
	maxBytes := flag.Int64("recordings-max-bytes", defaultArchiveMax, "size the recordings are aged down to, 0 for no limit")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("nvr: ")

	st, err := store.Open(*dataPath)
	if err != nil {
		log.Fatalf("open camera list: %v", err)
	}
	log.Printf("loaded %d camera(s) from %s", len(st.List()), *dataPath)

	// Signals are wired before anything long-running starts so an early Ctrl-C
	// is still a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The archive is where footage lives once it is off the camera it was
	// recorded on. Without one the service still watches and proxies cameras,
	// it just keeps nothing, which is what a run with no data volume is.
	var arch *archive.Store
	if *recDir != "" {
		arch, err = archive.Open(*recDir, *maxBytes)
		if err != nil {
			log.Fatalf("open recordings: %v", err)
		}
		usage, err := arch.Usage()
		if err != nil {
			log.Fatalf("read recordings: %v", err)
		}
		log.Printf("holding %d recording(s), %d MB of %d MB, in %s",
			usage.Recordings, usage.Bytes>>20, usage.MaxBytes>>20, arch.Root())
		go sweepArchive(ctx, arch)
	}

	mgr := manager.New(arch)
	mgr.Sync(st.List())

	disc := discovery.New()
	// Discovery runs entirely in the background. It must never delay serving,
	// and on a machine with no network it simply finds nothing.
	go disc.Run(ctx)

	// A camera added under its .local name is unreachable from inside the
	// container, which has no mDNS resolver, so stored addresses are kept
	// pointed at the IP discovery reports for them.
	go manager.WatchAddresses(ctx, st, mgr, disc, addressCheckEvery)

	api := httpapi.New(st, mgr, disc, arch, *staticDir)

	// Handler contexts derive from this one, so cancelling it ends in-flight
	// MJPEG streams instead of leaving Shutdown waiting on connections that
	// were designed never to finish.
	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
		log.Print("shutting down")
	}

	cancelRequests()
	mgr.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
		srv.Close()
	}
	log.Print("stopped")
}

// sweepArchive ages the archive down to its limit on a slow timer. Retention is
// the operator's business rather than the cameras': nothing here ever deletes
// anything from a camera's own card.
func sweepArchive(ctx context.Context, arch *archive.Store) {
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	// The first pass is immediate. An archive can be over its limit the moment
	// it is opened, because the limit is the operator's and they may have just
	// lowered it, and waiting out a five minute timer to act on that reads as
	// the setting having been ignored.
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		removed, freed, err := arch.Sweep()
		if err != nil {
			log.Printf("recordings: retention pass failed: %v", err)
			continue
		}
		if removed > 0 {
			log.Printf("recordings: aged out %d recording(s), %d MB", removed, freed>>20)
		}
	}
}
