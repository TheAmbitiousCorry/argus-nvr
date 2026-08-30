// Package camera talks to one ESP32-CAM: session login, JSON state, snapshots
// and the MJPEG stream.
package camera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"argus-nvr/internal/store"
)

// defaultStreamPort is fixed by the firmware: video is served from a second
// HTTP server on 81 while everything else lives on 80. Cookies ignore the port,
// so one session covers both.
const defaultStreamPort = "81"

// A round trip to a camera on Wi-Fi was measured at about three seconds, and a
// request that has to re-login pays that twice, so these are set well above the
// happy path rather than at it.
const (
	requestTimeout = 10 * time.Second
	loginTimeout   = 10 * time.Second
)

// uploadHeaderTimeout bounds the wait for the reply to a firmware upload. The
// camera erases and writes flash before it answers, so the gap between the last
// byte sent and the first byte of the response is much longer than any other
// request here, and cutting it short would report a successful flash as failed.
const uploadHeaderTimeout = 3 * time.Minute

// ErrUnauthorized means the camera rejected our credentials, as opposed to
// merely having forgotten the session.
var ErrUnauthorized = errors.New("camera rejected credentials")

// Client keeps an authenticated session for a single camera and re-establishes
// it transparently. Cameras reboot on every firmware update and settings
// change, and sessions live in their RAM, so losing the session is routine
// rather than exceptional.
type Client struct {
	cam store.Camera

	// streamPort is a field rather than a constant so the stream can be
	// pointed at a test server or a port-forwarded camera. Real firmware
	// always uses 81.
	streamPort string

	api      *http.Client
	stream   *http.Client
	upload   *http.Client
	download *http.Client

	mu  sync.Mutex
	sid string
	// gen increments on every successful login so that concurrent callers who
	// all saw the same dead session trigger exactly one re-login between them.
	// The cameras are microcontrollers; a login storm from a dozen in-flight
	// requests is exactly the kind of load they cannot absorb.
	gen uint64
}

// New builds a client for cam. Redirects are never followed: the firmware
// answers an expired session on a page route with 302 to /login, and following
// it would turn an auth failure into a confusing 200.
func New(cam store.Camera) *Client {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		cam:        cam,
		streamPort: defaultStreamPort,
		api: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: requestTimeout,
			},
		},
		upload: &http.Client{
			// No client timeout: the body is over a megabyte and the camera
			// writes it to flash before replying, so the bound is the
			// context the caller passes plus the header timeout below.
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				// The camera reboots the moment it has answered, so a pooled
				// connection here is always a dead one.
				DisableKeepAlives:     true,
				ResponseHeaderTimeout: uploadHeaderTimeout,
			},
		},
		download: &http.Client{
			// No client timeout: a recording runs to tens of megabytes off a
			// card, over a radio it is sharing with the live view, and a clip
			// that takes a minute to arrive is arriving rather than stuck.
			// What is bounded is the wait for the first byte, which is what
			// tells a slow camera apart from an absent one.
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				// The camera has a handful of sockets. A pooled connection
				// held open after a download is one a viewer cannot have.
				DisableKeepAlives:     true,
				ResponseHeaderTimeout: requestTimeout,
			},
		},
		stream: &http.Client{
			// No client timeout: an MJPEG response body is meant to never end.
			// The transport still bounds the time spent waiting for headers so
			// an unreachable camera cannot hang a viewer forever.
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				// A dropped viewer must free the camera's socket immediately.
				// The ESP32 has a handful of them, so an idle pooled connection
				// is a connection some other viewer cannot have.
				DisableKeepAlives:     true,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
	}
}

// Camera returns the configuration this client was built from.
func (c *Client) Camera() store.Camera { return c.cam }

func (c *Client) baseURL() string { return "http://" + c.cam.Address }

func (c *Client) streamURL(path string) string {
	return "http://" + net.JoinHostPort(c.cam.Host(), c.streamPort) + path
}

// Get issues an authenticated GET against the camera's port 80 API.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, c.api, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	})
}

// GetLarge issues an authenticated GET for a body too large to fetch under the
// ordinary request timeout, which bounds the whole exchange rather than the
// wait for a reply. The caller owns the body and must close it.
func (c *Client) GetLarge(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, c.download, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	})
}

// PostForm issues an authenticated form post against the camera's port 80 API.
// The firmware's form handlers take a whole form, so callers build the complete
// set of fields rather than only the ones they mean to change.
func (c *Client) PostForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	body := form.Encode()
	return c.do(ctx, c.api, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	})
}

// PostRaw posts bytes as the request body with Content-Length set, which is
// what /update wants: the firmware reads the image straight off the socket and
// does not parse multipart. The body is rebuilt on a retry, so a request that
// has to log in again is replayed intact.
func (c *Client) PostRaw(ctx context.Context, path, contentType string, body []byte) (*http.Response, error) {
	return c.do(ctx, c.upload, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		req.ContentLength = int64(len(body))
		return req, nil
	})
}

// OpenStream starts the MJPEG stream. The caller owns the response body and
// must close it; the returned response is deliberately not read here so the
// body can be piped straight to viewers.
func (c *Client) OpenStream(ctx context.Context) (*http.Response, error) {
	return c.do(ctx, c.stream, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.streamURL("/stream"), nil)
	})
}

// do runs a request with the current session and, if the camera says the
// session is gone, logs in once and replays it. The request is rebuilt rather
// than reused because a replayed request needs a fresh body and headers.
func (c *Client) do(ctx context.Context, hc *http.Client, build func(context.Context) (*http.Request, error)) (*http.Response, error) {
	sid, gen, err := c.session(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := c.attempt(ctx, hc, build, sid)
	if err != nil {
		return nil, err
	}
	if !sessionExpired(resp) {
		return resp, nil
	}

	// Drain and close before retrying so the connection can be reused or
	// closed cleanly rather than being abandoned mid-response.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	sid, err = c.relogin(ctx, gen)
	if err != nil {
		return nil, err
	}
	resp, err = c.attempt(ctx, hc, build, sid)
	if err != nil {
		return nil, err
	}
	if sessionExpired(resp) {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	return resp, nil
}

func (c *Client) attempt(ctx context.Context, hc *http.Client, build func(context.Context) (*http.Request, error), sid string) (*http.Response, error) {
	req, err := build(ctx)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	return hc.Do(req)
}

// sessionExpired distinguishes "log in again" from a real error. Resource
// routes answer an unauthenticated request with 401, page routes with a 302 to
// /login, and both mean the same thing.
func sessionExpired(resp *http.Response) bool {
	if resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		loc, err := url.Parse(resp.Header.Get("Location"))
		return err == nil && strings.HasPrefix(loc.Path, "/login")
	}
	return false
}

// session returns a usable sid, logging in if there is none yet.
func (c *Client) session(ctx context.Context) (sid string, gen uint64, err error) {
	c.mu.Lock()
	sid, gen = c.sid, c.gen
	c.mu.Unlock()
	if sid != "" {
		return sid, gen, nil
	}
	sid, err = c.relogin(ctx, gen)
	if err != nil {
		return "", 0, err
	}
	c.mu.Lock()
	gen = c.gen
	c.mu.Unlock()
	return sid, gen, nil
}

// relogin logs in unless another goroutine already replaced the session that
// the caller found dead, in which case the newer one is handed back untouched.
func (c *Client) relogin(ctx context.Context, staleGen uint64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != staleGen && c.sid != "" {
		return c.sid, nil
	}

	first := c.gen == 0
	sid, err := c.login(ctx)
	if err != nil {
		c.sid = ""
		return "", err
	}
	c.sid = sid
	c.gen++
	// Worth a line either way: a camera that re-logs in constantly is telling
	// you something is knocking its session out, and the device only keeps
	// three sessions before evicting the oldest.
	if first {
		log.Printf("camera %s: signed in", c.cam.Name)
	} else {
		log.Printf("camera %s: session was gone, signed in again", c.cam.Name)
	}
	return sid, nil
}

// login posts the credentials form and captures the sid cookie. The password
// is never logged, and the response body is discarded unread.
func (c *Client) login(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	form := url.Values{"user": {c.cam.User}, "pass": {c.cam.Pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.api.Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	for _, ck := range resp.Cookies() {
		if ck.Name == "sid" && ck.Value != "" {
			return ck.Value, nil
		}
	}
	// A login that answers without a cookie is the firmware's way of saying
	// the credentials were wrong; it re-renders the form instead of erroring.
	return "", ErrUnauthorized
}
