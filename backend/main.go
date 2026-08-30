// Command esp32cam-nvr aggregates several ESP32-CAM cameras behind one HTTP
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

	"esp32cam-nvr/internal/discovery"
	"esp32cam-nvr/internal/httpapi"
	"esp32cam-nvr/internal/manager"
	"esp32cam-nvr/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	dataPath := flag.String("data", "./data/cameras.json", "path to the camera list JSON file")
	staticDir := flag.String("static", "./web", "directory of built frontend files, empty to disable")
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

	mgr := manager.New()
	mgr.Sync(st.List())

	disc := discovery.New()
	// Discovery runs entirely in the background. It must never delay serving,
	// and on a machine with no network it simply finds nothing.
	go disc.Run(ctx)

	api := httpapi.New(st, mgr, disc, *staticDir)

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
