package camera

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// listingClient points a client at a handler standing in for a camera.
func listingClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return clientFor(t, srv)
}

func listing(body string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/recordings/days", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})
	return mux
}

// The firmware half of this is being written at the same time as this half,
// against the same page, which says what the answer holds without pinning the
// punctuation. All three readings of it are accepted, because the alternative
// is the two halves meeting and disagreeing over a bracket.
func TestDaysReadsTheObviousShapes(t *testing.T) {
	bodies := []string{
		`{"days":["2026-08-29","2026-08-30"]}`,
		`["2026-08-29","2026-08-30"]`,
		`{"days":[{"day":"2026-08-29","recordings":3},{"day":"2026-08-30","recordings":9}]}`,
	}
	for _, body := range bodies {
		c := listingClient(t, listing(body))
		days, err := c.Days(context.Background())
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if len(days) != 2 || days[0] != "2026-08-29" || days[1] != "2026-08-30" {
			t.Errorf("%s: read %v", body, days)
		}
	}
}

// A day becomes a directory name on this side, so what a camera says it holds
// is checked rather than trusted.
func TestDaysDropsWhatIsNotADate(t *testing.T) {
	c := listingClient(t, listing(`{"days":["2026-08-30","../../etc","","tomorrow"]}`))
	days, err := c.Days(context.Background())
	if err != nil {
		t.Fatalf("days: %v", err)
	}
	if len(days) != 1 || days[0] != "2026-08-30" {
		t.Errorf("kept %v", days)
	}
}

// A camera running firmware from before the listing existed answers 404, and
// that is a state to report rather than an error to retry hard.
func TestMissingListingIsItsOwnAnswer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such route", http.StatusNotFound)
	})
	c := listingClient(t, mux)

	if _, err := c.Days(context.Background()); !errors.Is(err, ErrNoListing) {
		t.Errorf("days: %v", err)
	}
	if _, _, err := c.Recordings(context.Background(), "2026-08-30", 0); !errors.Is(err, ErrNoListing) {
		t.Errorf("recordings: %v", err)
	}
}

func TestRecordingsPagesWithStart(t *testing.T) {
	var asked string
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/recordings", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		io.WriteString(w, `{"day":"2026-08-30","recordings":[{"at":"131529","durMs":10004,"bytes":12256740,"frames":213}],"more":true}`)
	})
	c := listingClient(t, mux)

	recs, more, err := c.Recordings(context.Background(), "2026-08-30", 50)
	if err != nil {
		t.Fatalf("recordings: %v", err)
	}
	if !strings.Contains(asked, "day=2026-08-30") || !strings.Contains(asked, "start=50") {
		t.Errorf("asked %q", asked)
	}
	if !more || len(recs) != 1 || recs[0].At != "131529" || recs[0].Frames != 213 {
		t.Errorf("read %+v more=%v", recs, more)
	}
}

// The download is of the recording named, at the path the firmware already
// serves recordings from.
func TestOpenRecordingAsksForTheRightDirectory(t *testing.T) {
	var asked string
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/video", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("dir")
		io.WriteString(w, "RIFF")
	})
	c := listingClient(t, mux)

	resp, err := c.OpenRecording(context.Background(), "2026-08-30", "131529")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer resp.Body.Close()
	if asked != "/rec/2026-08-30/131529" {
		t.Errorf("asked for %q", asked)
	}
}
