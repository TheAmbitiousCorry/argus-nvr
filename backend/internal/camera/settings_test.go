package camera

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"argus-nvr/internal/store"
)

// liveConfig is what a camera on the development network actually returns, so
// the merge is tested against the document it will really be handed.
const liveConfig = `{
  "camname":"camera-alpha","tz":"SAST-2","ssid":"CorVic-19 ","apwin":true,
  "moten":true,"motsens":15,"recsec":6,"presec":5,"quietsec":5,"keepfree":800,
  "schen":true,"schfrom":0,"schto":0,"schdays":1,
  "fsize":10,"jq":14,"autoimg":true,
  "ael":2,"gc":6,"bri":0,"con":0,"sat":0,"wb":0,
  "gray":false,"hmir":false,"vflip":true,"flashlvl":130,
  "aelnow":2,"gcnow":3,"unsupported":""
}`

func fields(t *testing.T, doc string) map[string]json.RawMessage {
	t.Helper()
	if doc == "" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("parse %s: %v", doc, err)
	}
	return m
}

// The whole point of the merge: a form is posted in full, so anything the
// caller did not name has to come back exactly as the camera already had it.
func TestBuildImageForm(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		patch   string
		want    string
		wantErr bool
	}{
		{
			name:   "one field changes and nothing else moves",
			config: liveConfig,
			patch:  `{"bri":1}`,
			want:   "ael=2&autoimg=1&bri=1&con=0&flashlvl=130&gc=6&sat=0&vflip=1&wb=0",
		},
		{
			name:   "unticking a checkbox leaves the field out",
			config: liveConfig,
			patch:  `{"autoimg":false}`,
			want:   "ael=2&bri=0&con=0&flashlvl=130&gc=6&sat=0&vflip=1&wb=0",
		},
		{
			name:   "ticking a checkbox that was off",
			config: liveConfig,
			patch:  `{"gray":true}`,
			want:   "ael=2&autoimg=1&bri=0&con=0&flashlvl=130&gc=6&gray=1&sat=0&vflip=1&wb=0",
		},
		{
			name:   "several fields at once",
			config: liveConfig,
			patch:  `{"autoimg":false,"ael":-1,"gc":0,"flashlvl":0}`,
			want:   "ael=-1&bri=0&con=0&flashlvl=0&gc=0&sat=0&vflip=1&wb=0",
		},
		{
			name:   "read-only sensor fields are never posted back",
			config: `{"ael":1,"aelnow":2,"gcnow":4,"unsupported":"gc","bri":0}`,
			patch:  `{"bri":1}`,
			want:   "ael=1&bri=1",
		},
		{
			name:   "recording fields are not in the image form",
			config: liveConfig,
			patch:  `{}`,
			want:   "ael=2&autoimg=1&bri=0&con=0&flashlvl=130&gc=6&sat=0&vflip=1&wb=0",
		},
		{
			name:   "a field neither side knows is left out",
			config: `{"bri":0}`,
			patch:  `{"bri":1}`,
			want:   "bri=1",
		},
		{
			name:   "a field only a later firmware knows is passed through",
			config: `{"bri":0}`,
			patch:  `{"bri":0,"sharpness":3}`,
			want:   "bri=0&sharpness=3",
		},
		{
			name:   "the firmware's own 1 and 0 read as a checkbox",
			config: `{"autoimg":1,"gray":0,"bri":0}`,
			patch:  `{"bri":1}`,
			want:   "autoimg=1&bri=1",
		},
		{
			name:    "a value that is not a number is refused rather than guessed",
			config:  liveConfig,
			patch:   `{"bri":"quite bright"}`,
			wantErr: true,
		},
		{
			name:    "a fractional value is refused",
			config:  liveConfig,
			patch:   `{"flashlvl":1.5}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form, err := BuildImageForm(fields(t, tt.config), fields(t, tt.patch))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got form %q", form.Encode())
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := form.Encode(); got != tt.want {
				t.Errorf("form\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestBuildRecordingForm(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		patch   string
		want    string
		wantErr bool
	}{
		{
			name:   "one field changes and the schedule survives",
			config: liveConfig,
			patch:  `{"motsens":20}`,
			want:   "fsize=10&jq=14&keepfree=800&moten=1&motsens=20&presec=5&quietsec=5&recsec=6&schday=0&schen=1&schfrom=0&schto=0",
		},
		{
			name:   "the day bitmask becomes one repeated field per day",
			config: `{"schdays":127,"schen":true,"schfrom":22,"schto":6}`,
			patch:  `{"schfrom":21}`,
			want:   "schday=0&schday=1&schday=2&schday=3&schday=4&schday=5&schday=6&schen=1&schfrom=21&schto=6",
		},
		{
			name:   "weekends only",
			config: `{"schdays":65,"schen":true}`,
			patch:  `{"schen":true}`,
			want:   "schday=0&schday=6&schen=1",
		},
		{
			name:   "a new bitmask replaces the stored one",
			config: `{"schdays":127,"schen":true}`,
			patch:  `{"schdays":6}`,
			want:   "schday=1&schday=2&schen=1",
		},
		{
			name:   "an explicit list of days is accepted too",
			config: `{"schdays":127,"schen":true}`,
			patch:  `{"schday":[3,1,1]}`,
			want:   "schday=1&schday=3&schen=1",
		},
		{
			name:   "no days enabled sends no schday at all",
			config: `{"schdays":0,"schen":false,"recsec":10}`,
			patch:  `{"recsec":12}`,
			want:   "recsec=12",
		},
		{
			name:   "turning motion off leaves its checkbox out",
			config: liveConfig,
			patch:  `{"moten":false}`,
			want:   "fsize=10&jq=14&keepfree=800&motsens=15&presec=5&quietsec=5&recsec=6&schday=0&schen=1&schfrom=0&schto=0",
		},
		{
			name:   "image fields are not in the recording form",
			config: liveConfig,
			patch:  `{"jq":10}`,
			want:   "fsize=10&jq=10&keepfree=800&moten=1&motsens=15&presec=5&quietsec=5&recsec=6&schday=0&schen=1&schfrom=0&schto=0",
		},
		{
			name:    "a day outside the week is refused",
			config:  `{"schdays":1}`,
			patch:   `{"schday":[9]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form, err := BuildRecordingForm(fields(t, tt.config), fields(t, tt.patch))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got form %q", form.Encode())
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := form.Encode(); got != tt.want {
				t.Errorf("form\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// fakeSettings is a camera that keeps its settings, so a post can be checked
// against the config that follows it rather than only against the form.
type fakeSettings struct {
	mu     sync.Mutex
	config map[string]any
	posts  []url.Values
	// fail makes /config or the form handlers answer with a server error, for
	// the paths where the camera is reachable but unhappy.
	failConfig bool
	failPost   bool
}

func newFakeSettings() *fakeSettings {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(liveConfig), &cfg); err != nil {
		panic(err)
	}
	return &fakeSettings{config: cfg}
}

func (f *fakeSettings) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "sid1", Path: "/"})
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if f.failConfig {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.config)
	})
	mux.HandleFunc("/image", f.form(imageFields))
	mux.HandleFunc("/recording", f.form(recordingFields))
	return mux
}

// form applies a whole posted form the way the firmware does: every checkbox in
// the handler's own list is set from whether the field arrived, so a field left
// out is a field switched off.
func (f *fakeSettings) form(known []field) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.failPost {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()
		f.posts = append(f.posts, r.PostForm)
		for _, fd := range known {
			switch fd.kind {
			case kindCheckbox:
				f.config[fd.name] = r.PostForm.Has(fd.name)
			default:
				if v := r.PostForm.Get(fd.name); v != "" {
					var n json.Number = json.Number(v)
					i, err := n.Int64()
					if err != nil {
						http.Error(w, "bad value", http.StatusBadRequest)
						return
					}
					f.config[fd.name] = float64(i)
				}
			}
		}
		if r.URL.Path == "/recording" {
			mask := 0
			for _, d := range r.PostForm["schday"] {
				var n json.Number = json.Number(d)
				i, _ := n.Int64()
				mask |= 1 << i
			}
			f.config["schdays"] = float64(mask)
		}
		io.WriteString(w, "ok")
	}
}

func (f *fakeSettings) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]any, len(f.config))
	for k, v := range f.config {
		out[k] = v
	}
	return out
}

func settingsClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(store.Camera{
		Address: strings.TrimPrefix(srv.URL, "http://"),
		Name:    "fake",
		User:    "admin",
		Pass:    "secret",
	})
}

// The failure this guards against is silent: the settings the caller did not
// mention come back reset, and nothing errors.
func TestApplySettingsChangesOnlyWhatWasAsked(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
		want     map[string]any
	}{
		{
			name:     "an image field",
			settings: Settings{Image: fields(t, `{"bri":1}`)},
			want:     map[string]any{"bri": 1.0},
		},
		{
			name:     "a recording field",
			settings: Settings{Recording: fields(t, `{"motsens":20}`)},
			want:     map[string]any{"motsens": 20.0},
		},
		{
			name:     "turning a checkbox off",
			settings: Settings{Image: fields(t, `{"autoimg":false}`)},
			want:     map[string]any{"autoimg": false},
		},
		{
			name:     "both forms at once",
			settings: Settings{Image: fields(t, `{"gray":true}`), Recording: fields(t, `{"jq":10}`)},
			want:     map[string]any{"gray": true, "jq": 10.0},
		},
		{
			name:     "the schedule days",
			settings: Settings{Recording: fields(t, `{"schdays":65}`)},
			want:     map[string]any{"schdays": 65.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeSettings()
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()

			before := fake.snapshot()
			if err := settingsClient(t, srv).ApplySettings(context.Background(), tt.settings); err != nil {
				t.Fatalf("apply: %v", err)
			}
			after := fake.snapshot()

			for k, want := range tt.want {
				if after[k] != want {
					t.Errorf("%s: got %v, want %v", k, after[k], want)
				}
			}
			for k, was := range before {
				if _, asked := tt.want[k]; asked {
					continue
				}
				if after[k] != was {
					t.Errorf("%s moved on its own: was %v, now %v", k, was, after[k])
				}
			}
		})
	}
}

// A patch for one form must not cause the other form to be posted at all,
// because posting it would rewrite settings nobody asked about.
func TestApplySettingsPostsOnlyTheFormItNeeds(t *testing.T) {
	fake := newFakeSettings()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := settingsClient(t, srv).ApplySettings(context.Background(), Settings{
		Image: fields(t, `{"bri":1}`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.posts) != 1 {
		t.Fatalf("expected one form post, got %d", len(fake.posts))
	}
}

func TestApplySettingsErrors(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
		prepare  func(*fakeSettings)
	}{
		{
			name:     "the camera will not give up its config",
			settings: Settings{Image: fields(t, `{"bri":1}`)},
			prepare:  func(f *fakeSettings) { f.failConfig = true },
		},
		{
			name:     "the camera refuses the form",
			settings: Settings{Image: fields(t, `{"bri":1}`)},
			prepare:  func(f *fakeSettings) { f.failPost = true },
		},
		{
			name:     "the patch is not a value the firmware could take",
			settings: Settings{Image: fields(t, `{"bri":{"nested":true}}`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeSettings()
			if tt.prepare != nil {
				tt.prepare(fake)
			}
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()

			if err := settingsClient(t, srv).ApplySettings(context.Background(), tt.settings); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// An empty update touches nothing at all, rather than posting two forms full of
// the values it just read back.
func TestApplySettingsWithNothingToDoTouchesNothing(t *testing.T) {
	fake := newFakeSettings()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := settingsClient(t, srv).ApplySettings(context.Background(), Settings{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.posts) != 0 {
		t.Fatalf("expected no posts, got %d", len(fake.posts))
	}
}

func TestConfigIsPassedThroughUnchanged(t *testing.T) {
	fake := newFakeSettings()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	got, err := settingsClient(t, srv).Config(context.Background())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var have, want map[string]any
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if err := json.Unmarshal([]byte(liveConfig), &want); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s: got %v, want %v", k, have[k], v)
		}
	}
}
