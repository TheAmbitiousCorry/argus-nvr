package camera

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"argus-nvr/internal/store"
)

// fakeCamera imitates the firmware's session behaviour: a login hands out an
// sid, and rebooting forgets every sid it ever issued.
type fakeCamera struct {
	mu     sync.Mutex
	valid  map[string]bool
	next   int
	logins int32
	// pageRoute makes an expired session answer with 302 to /login instead of
	// 401, which is what the firmware does for page routes.
	pageRoute bool
}

func newFakeCamera() *fakeCamera {
	return &fakeCamera{valid: make(map[string]bool)}
}

func (f *fakeCamera) reboot() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = make(map[string]bool)
}

func (f *fakeCamera) loginCount() int32 { return atomic.LoadInt32(&f.logins) }

func (f *fakeCamera) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.logins, 1)
		if r.FormValue("user") != "admin" || r.FormValue("pass") != "secret" {
			w.WriteHeader(http.StatusOK)
			return
		}
		f.mu.Lock()
		f.next++
		sid := fmt.Sprintf("sid%d", f.next)
		f.valid[sid] = true
		f.mu.Unlock()

		http.SetCookie(w, &http.Cookie{Name: "sid", Value: sid, Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/record", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorised(r) {
			if f.pageRoute {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"active":false}`)
	})
	return mux
}

func (f *fakeCamera) authorised(r *http.Request) bool {
	ck, err := r.Cookie("sid")
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.valid[ck.Value]
}

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(store.Camera{
		Address: strings.TrimPrefix(srv.URL, "http://"),
		User:    "admin",
		Pass:    "secret",
	})
}

func getBody(t *testing.T, c *Client, path string) string {
	t.Helper()
	resp, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestSessionIsReused(t *testing.T) {
	fake := newFakeCamera()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := clientFor(t, srv)
	for i := 0; i < 5; i++ {
		getBody(t, c, "/record")
	}
	if n := fake.loginCount(); n != 1 {
		t.Fatalf("expected one login for five requests, got %d", n)
	}
}

func TestReloginAfterReboot(t *testing.T) {
	for _, pageRoute := range []bool{false, true} {
		name := "401"
		if pageRoute {
			name = "302-to-login"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFakeCamera()
			fake.pageRoute = pageRoute
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()

			c := clientFor(t, srv)
			getBody(t, c, "/record")

			fake.reboot()

			if got := getBody(t, c, "/record"); got != `{"active":false}` {
				t.Fatalf("after reboot got %q", got)
			}
			if n := fake.loginCount(); n != 2 {
				t.Fatalf("expected exactly one silent re-login, got %d logins", n)
			}
		})
	}
}

// A reboot strands every in-flight request at once. Only one of them should
// log in again, because a login storm is what the microcontroller cannot take.
func TestConcurrentReloginHappensOnce(t *testing.T) {
	fake := newFakeCamera()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := clientFor(t, srv)
	getBody(t, c, "/record")
	fake.reboot()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(context.Background(), "/record")
			if err != nil {
				t.Errorf("concurrent get: %v", err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()

	if n := fake.loginCount(); n != 2 {
		t.Fatalf("expected 2 logins total (initial plus one shared re-login), got %d", n)
	}
}

func TestBadCredentials(t *testing.T) {
	fake := newFakeCamera()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := New(store.Camera{
		Address: strings.TrimPrefix(srv.URL, "http://"),
		User:    "admin",
		Pass:    "wrong",
	})
	if _, err := c.Get(context.Background(), "/record"); err == nil {
		t.Fatal("expected an error for wrong credentials")
	}
}
