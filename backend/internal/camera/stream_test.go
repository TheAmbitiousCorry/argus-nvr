package camera

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"argus-nvr/internal/store"
)

// fakeStream serves an endless MJPEG stream and counts how many clients are
// connected at once, which is the number the fan-out is meant to hold at one.
type fakeStream struct {
	open   int32
	opened int32
}

func (f *fakeStream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.open, 1)
		atomic.AddInt32(&f.opened, 1)
		defer atomic.AddInt32(&f.open, -1)

		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+Boundary)
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)

		jpeg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", Boundary, len(jpeg))
			if _, err := w.Write(jpeg); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return mux
}

// hubFor points both the API and the stream at the one test server, since a
// test cannot bind the firmware's privileged-adjacent port 81.
func hubFor(t *testing.T, srv *httptest.Server) *Hub {
	t.Helper()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	c := New(store.Camera{Address: hostport, User: "admin", Pass: "secret"})
	c.streamPort = port
	return NewHub(c)
}

// Many viewers must cost the camera exactly one connection, because the device
// halves its frame rate for every extra client.
func TestManyViewersOneUpstream(t *testing.T) {
	fake := &fakeStream{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	hub := hubFor(t, srv)
	defer hub.Close()

	var cancels []func()
	for i := 0; i < 6; i++ {
		ch, cancel := hub.Subscribe()
		cancels = append(cancels, cancel)
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("viewer %d received no frame", i)
		}
	}
	if got := atomic.LoadInt32(&fake.open); got != 1 {
		t.Fatalf("expected 1 upstream connection for 6 viewers, got %d", got)
	}
	if got := hub.Viewers(); got != 6 {
		t.Fatalf("expected 6 viewers, got %d", got)
	}

	for _, c := range cancels {
		c()
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&fake.open) == 0 },
		"upstream connection stayed open after the last viewer left")
}

// A closed browser tab must not leave the connection to the camera running.
func TestNoLeakAcrossViewerChurn(t *testing.T) {
	fake := &fakeStream{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	hub := hubFor(t, srv)
	defer hub.Close()

	before := runtime.NumGoroutine()
	for i := 0; i < 25; i++ {
		ch, cancel := hub.Subscribe()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("no frame")
		}
		cancel()
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&fake.open) == 0 },
		"upstream still open after churn")

	waitFor(t, func() bool { return runtime.NumGoroutine() <= before+3 },
		fmt.Sprintf("goroutines grew from %d during viewer churn", before))
}

// A snapshot must not open a second connection when the stream is already up.
func TestSnapshotReusesLiveStream(t *testing.T) {
	fake := &fakeStream{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	hub := hubFor(t, srv)
	defer hub.Close()

	ch, cancel := hub.Subscribe()
	defer cancel()
	<-ch

	opened := atomic.LoadInt32(&fake.opened)
	if _, err := hub.Frame(context.Background()); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := atomic.LoadInt32(&fake.opened); got != opened {
		t.Fatalf("snapshot opened a second connection: %d -> %d", opened, got)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
