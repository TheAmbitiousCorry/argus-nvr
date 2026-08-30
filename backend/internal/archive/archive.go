// Package archive holds the recordings the service has taken off a camera's
// card, or written on the camera's behalf, on the data volume.
//
// A recording is identified by the camera it came from, the day it was made
// and the time it started, and that identity is the path it is stored at:
// <root>/<camera id>/<day>/<start time>.<avi|mp4>. Nothing else is needed to
// say whether a recording is already held, which is what makes pulling
// idempotent after an outage that left the service and a card disagreeing about
// what exists. The extension is the form it is held in and not part of the
// identity, so transcoding one changes what plays it and nothing else.
//
// Every write lands on a temp file and is renamed into place, so an interrupted
// download or a power cut mid-recording leaves either the whole recording or
// nothing at all, never a half file under a name that promises a whole one.
package archive

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"argus-nvr/internal/avi"
)

// Source says how a recording came to be here. It is worth keeping because the
// two are not equivalent: one is a copy of something that also exists on a
// card, and the other is the only copy there is.
const (
	SourceCamera  = "camera"  // pulled from the camera's own card
	SourceService = "service" // written here from the live stream, for a camera that could not record
)

// A recording is held in one of two forms, and its form is its extension. It
// arrives as MJPEG in AVI, because that is what the cameras write, and it is
// re-encoded to H.264 in MP4 once it is safely stored, because that is a
// fraction of the size and the only one of the two a browser can play.
//
// The identity does not change with the form: same camera, same day, same
// start time, a different extension. Nothing that already points at a recording
// breaks when it is transcoded.
const (
	FormatAVI = "avi"
	FormatMP4 = "mp4"
)

// formats is the order a recording is looked for in. MP4 first: while a
// transcode is being finished both may exist for an instant, and the MP4 is the
// one that was verified.
var formats = []string{FormatMP4, FormatAVI}

// agedFile is where a day records what retention has taken from it.
//
// Without it the two halves of this feature fight: retention ages a recording
// out to stay under the size limit, the puller sees the camera still has it on
// its card and has no copy, and downloads it again, forever. One small file a
// day is a cheap price for that loop not existing. It is a note about the
// archive's own history, not about the card, and nothing on the card is
// touched either way.
const agedFile = ".aged"

// tempPrefix marks a write in progress. It is hidden and unmistakable: nothing
// listing recordings will show one, retention will never delete one, and the
// leftovers of a crash can be swept at startup without guessing.
const tempPrefix = ".part-"

const (
	dayLen = len("2006-01-02")
	atLen  = len("150405")
)

// ErrNotFound is returned for a recording the service does not hold.
var ErrNotFound = errors.New("recording not found")

// ID is a recording's identity: the camera, the day, and the time it started.
type ID struct {
	CameraID string
	Day      string // YYYY-MM-DD
	At       string // HHMMSS
}

func (id ID) String() string { return id.CameraID + "/" + id.Day + "/" + id.At }

// valid rejects anything that is not exactly the shape of an identity. Days and
// times arrive from a camera's own answers and become path segments here, so
// this is what stops a camera, or something pretending to be one, writing
// outside the archive.
func (id ID) valid() bool {
	return validCameraID(id.CameraID) && ValidDay(id.Day) && ValidAt(id.At)
}

func validCameraID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// ValidDay reports whether s is a YYYY-MM-DD the archive will accept.
func ValidDay(s string) bool {
	if len(s) != dayLen {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ValidAt reports whether s is the HHMMSS the firmware names recordings with.
func ValidAt(s string) bool {
	if len(s) != atLen {
		return false
	}
	_, err := time.Parse("150405", s)
	return err == nil
}

// Meta is what the AVI itself cannot say: how long the recording ran, how many
// frames it holds, and where it came from. It sits beside the recording as its
// own small file so that adding one recording never rewrites another.
type Meta struct {
	DurMs  int64     `json:"durMs"`
	Frames int       `json:"frames"`
	Source string    `json:"source"`
	HeldAt time.Time `json:"heldAt"`
}

// Recording is one held recording as the API reports it.
type Recording struct {
	CameraID string `json:"cameraId"`
	Day      string `json:"day"`
	At       string `json:"at"`
	// StartedAt carries no zone on purpose. It is the camera's own clock, and
	// this service does not know what that clock is set to; stamping it with
	// the server's zone would be an invention rather than a conversion.
	StartedAt string `json:"startedAt"`
	DurMs     int64  `json:"durMs"`
	Bytes     int64  `json:"bytes"`
	Frames    int    `json:"frames,omitempty"`
	Source    string `json:"source"`
	// Format is `avi` or `mp4`. It is what a caller reads to know whether this
	// recording can be handed to a video element or has to be replayed frame by
	// frame, which is the whole difference between the two.
	Format string    `json:"format"`
	HeldAt time.Time `json:"heldAt"`
}

// ID rebuilds the recording's identity.
func (r Recording) ID() ID { return ID{CameraID: r.CameraID, Day: r.Day, At: r.At} }

// Day is one day's worth of a camera's recordings, for the listing that lets a
// caller ask for a day without first fetching every recording in it.
type Day struct {
	CameraID   string `json:"cameraId"`
	Day        string `json:"day"`
	Recordings int    `json:"recordings"`
	Bytes      int64  `json:"bytes"`
}

// Usage is what retention is deciding against.
type Usage struct {
	Bytes      int64 `json:"bytes"`
	MaxBytes   int64 `json:"maxBytes"`
	Recordings int   `json:"recordings"`
	// Transcoded is how many of them are H.264 in MP4 rather than the MJPEG in
	// AVI they arrived as. It is the progress of a backfill, and the number that
	// says how much of the archive a browser can play directly.
	Transcoded int `json:"transcoded"`
	// Pending is bytes belonging to writes that have not finished. They count
	// against the limit, because the disk does not care that they are not
	// finished, but they are never deleted to get under it.
	Pending int64 `json:"pendingBytes"`
}

// Store is the archive on disk. It holds no index: the layout is the index, so
// there is nothing to rebuild after a crash and nothing that can disagree with
// what is actually on the volume.
type Store struct {
	root     string
	maxBytes int64

	// mu serialises directory creation and the retention sweep against each
	// other. Reads of finished recordings need no lock, because a finished
	// recording is never rewritten, only removed.
	mu sync.Mutex
}

// Open prepares the archive root and clears the leavings of any interrupted
// write. maxBytes is the size the whole archive is aged down to; zero or less
// means never age anything out.
func Open(root string, maxBytes int64) (*Store, error) {
	if root == "" {
		return nil, errors.New("archive: root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("archive: create %s: %w", root, err)
	}
	s := &Store{root: root, maxBytes: maxBytes}
	if err := s.sweepTemps(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root is where the archive lives, for logging.
func (s *Store) Root() string { return s.root }

// MaxBytes is the size the archive is kept under.
func (s *Store) MaxBytes() int64 { return s.maxBytes }

// sweepTemps removes part files from a previous run. Only one process owns the
// archive, so anything left with this prefix at startup is a write that did not
// survive, and keeping it would be paying disk for a file nothing can play.
func (s *Store) sweepTemps() error {
	removed := 0
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), tempPrefix) {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("archive: scan %s: %w", s.root, err)
	}
	return nil
}

func (s *Store) dayDir(cameraID, day string) string {
	return filepath.Join(s.root, cameraID, day)
}

// File is where a recording of the given form is or would be stored.
func (s *Store) File(id ID, format string) string {
	return filepath.Join(s.dayDir(id.CameraID, id.Day), id.At+"."+format)
}

// Locate reports which form of a recording is actually on the disk, and where.
// A recording is looked for in both forms because a half transcoded archive is
// the normal state of one that is being transcoded at all.
func (s *Store) Locate(id ID) (path, format string, ok bool) {
	if !id.valid() {
		return "", "", false
	}
	for _, f := range formats {
		p := s.File(id, f)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, f, true
		}
	}
	return "", "", false
}

func (s *Store) metaPath(id ID) string {
	return filepath.Join(s.dayDir(id.CameraID, id.Day), id.At+".json")
}

// Has reports whether the recording is already held. This is the whole of the
// "do not fetch it twice" rule, and it survives a restart because it asks the
// disk rather than any memory of what was pulled.
func (s *Store) Has(id ID) bool {
	_, _, ok := s.Locate(id)
	return ok
}

// Held returns the start times a camera's day is settled about: the ones held,
// and the ones retention has already aged out. A puller compares a whole day's
// listing against this in one directory read, and neither fetches a recording
// twice nor fetches back one that was deliberately let go.
func (s *Store) Held(cameraID, day string) (map[string]bool, error) {
	held := make(map[string]bool)
	entries, err := os.ReadDir(s.dayDir(cameraID, day))
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if at, _, ok := recordingName(e); ok {
			held[at] = true
		}
	}
	for _, at := range s.agedOut(cameraID, day) {
		held[at] = true
	}
	return held, nil
}

// agedOut reads the day's note of what retention has taken.
func (s *Store) agedOut(cameraID, day string) []string {
	data, err := os.ReadFile(filepath.Join(s.dayDir(cameraID, day), agedFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if at := strings.TrimSpace(line); ValidAt(at) {
			out = append(out, at)
		}
	}
	return out
}

// noteAged appends one start time to the day's note. A line that does not
// survive a crash costs one recording being pulled again, which is why this is
// an append rather than a rewrite of the whole file.
func (s *Store) noteAged(id ID) {
	f, err := os.OpenFile(filepath.Join(s.dayDir(id.CameraID, id.Day), agedFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(id.At + "\n")
}

// recordingName picks the finished recordings out of a directory listing, and
// says which form each is in. A name that is not exactly a start time and one
// of the two extensions is not a recording, which is what keeps part files and
// sidecars out of every listing at once.
func recordingName(e os.DirEntry) (at, format string, ok bool) {
	if e.IsDir() {
		return "", "", false
	}
	name := e.Name()
	for _, f := range formats {
		if !strings.HasSuffix(name, "."+f) {
			continue
		}
		at := strings.TrimSuffix(name, "."+f)
		if !ValidAt(at) {
			return "", "", false
		}
		return at, f, true
	}
	return "", "", false
}

// Open returns the recording's file for serving, and the form it is in, which
// is what decides the content type it goes out under. The caller closes it.
func (s *Store) Open(id ID) (*os.File, os.FileInfo, string, error) {
	path, format, ok := s.Locate(id)
	if !ok {
		return nil, nil, "", ErrNotFound
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, "", ErrNotFound
	}
	if err != nil {
		return nil, nil, "", err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return f, st, format, nil
}

// Save writes a recording that arrives as a ready-made AVI, which is what the
// camera's /video endpoint hands over.
//
// The bytes are checked before the file is given its real name: an AVI states
// its own total length in its second field, so a download cut short by a camera
// reboot or a dropped radio link is caught here rather than months later by
// whoever tries to play it.
func (s *Store) Save(id ID, r io.Reader, meta Meta) (int64, error) {
	if !id.valid() {
		return 0, fmt.Errorf("archive: refusing to store %q: not a recording identity", id)
	}
	if err := s.mkdirDay(id); err != nil {
		return 0, err
	}

	tmp, err := os.CreateTemp(s.dayDir(id.CameraID, id.Day), tempPrefix+id.At+"-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	var head [12]byte
	n, err := io.ReadFull(r, head[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if n < len(head) || string(head[0:4]) != "RIFF" || string(head[8:12]) != "AVI " {
		return 0, errors.New("archive: the camera did not send an AVI")
	}
	if _, err := tmp.Write(head[:n]); err != nil {
		return 0, err
	}
	rest, err := io.Copy(tmp, r)
	if err != nil {
		return 0, err
	}

	total := int64(n) + rest
	// RIFF states the length of everything after its own tag and this field.
	if want := int64(binary.LittleEndian.Uint32(head[4:8])) + 8; want != total {
		return 0, fmt.Errorf("archive: recording is %d bytes but says it is %d, so it arrived incomplete", total, want)
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}

	if meta.HeldAt.IsZero() {
		meta.HeldAt = time.Now()
	}
	if err := s.writeMeta(id, meta); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), s.File(id, FormatAVI)); err != nil {
		return 0, err
	}
	return total, nil
}

// Pending is a recording being written from the live stream. It exists under a
// temp name until Commit, so nothing lists it, retention cannot delete it, and
// a crash leaves it to be swept rather than served.
type Pending struct {
	store *Store
	id    ID
	file  *os.File
	avi   *avi.Writer
	done  bool
}

// Create starts a recording the service is making on a camera's behalf.
func (s *Store) Create(id ID) (*Pending, error) {
	if !id.valid() {
		return nil, fmt.Errorf("archive: refusing to record as %q: not a recording identity", id)
	}
	if err := s.mkdirDay(id); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.dayDir(id.CameraID, id.Day), tempPrefix+id.At+"-*")
	if err != nil {
		return nil, err
	}
	w, err := avi.NewWriter(f)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &Pending{store: s, id: id, file: f, avi: w}, nil
}

// ID is the identity this recording will be committed under.
func (p *Pending) ID() ID { return p.id }

// WriteFrame appends one frame from the stream.
func (p *Pending) WriteFrame(jpeg []byte, at time.Time) error {
	return p.avi.WriteFrame(jpeg, at)
}

// Frames reports how many frames have been written so far.
func (p *Pending) Frames() int { return p.avi.Frames() }

// Duration is the span the frames arrived over.
func (p *Pending) Duration() time.Duration { return p.avi.Duration() }

// Commit finishes the AVI and moves it into place under its real name, which is
// the moment it becomes a recording the service holds.
func (p *Pending) Commit() (Recording, error) {
	if p.done {
		return Recording{}, errors.New("archive: recording already finished")
	}
	p.done = true

	if err := p.avi.Close(); err != nil {
		p.discard()
		return Recording{}, err
	}
	if err := p.file.Sync(); err != nil {
		p.discard()
		return Recording{}, err
	}
	if err := p.file.Close(); err != nil {
		os.Remove(p.file.Name())
		return Recording{}, err
	}

	meta := Meta{
		DurMs:  p.avi.Duration().Milliseconds(),
		Frames: p.avi.Frames(),
		Source: SourceService,
		HeldAt: time.Now(),
	}
	if err := p.store.writeMeta(p.id, meta); err != nil {
		os.Remove(p.file.Name())
		return Recording{}, err
	}
	if err := os.Rename(p.file.Name(), p.store.File(p.id, FormatAVI)); err != nil {
		os.Remove(p.file.Name())
		return Recording{}, err
	}

	rec := Recording{
		CameraID:  p.id.CameraID,
		Day:       p.id.Day,
		At:        p.id.At,
		StartedAt: startedAt(p.id),
		DurMs:     meta.DurMs,
		Frames:    meta.Frames,
		Source:    meta.Source,
		HeldAt:    meta.HeldAt,
	}
	if st, err := os.Stat(p.store.File(p.id, FormatAVI)); err == nil {
		rec.Bytes = st.Size()
	}
	return rec, nil
}

// Abort throws the partial recording away. A recording too short to be worth
// keeping, or one whose stream died, leaves nothing behind.
func (p *Pending) Abort() {
	if p.done {
		return
	}
	p.done = true
	p.discard()
}

func (p *Pending) discard() {
	p.file.Close()
	os.Remove(p.file.Name())
}

func (s *Store) mkdirDay(id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.MkdirAll(s.dayDir(id.CameraID, id.Day), 0o700)
}

// writeMeta lands the sidecar before the recording is renamed into place, so a
// listing never sees a recording whose details are still missing. A sidecar
// left without its recording is swept by the next retention pass.
func (s *Store) writeMeta(id ID, meta Meta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	dir := s.dayDir(id.CameraID, id.Day)
	tmp, err := os.CreateTemp(dir, tempPrefix+id.At+"-meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.metaPath(id))
}

func (s *Store) readMeta(id ID) Meta {
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		// A recording with no sidecar is still a recording. Reporting it with
		// what the disk can say beats hiding footage because a small file went
		// missing.
		return Meta{Source: "unknown"}
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{Source: "unknown"}
	}
	if m.Source == "" {
		m.Source = "unknown"
	}
	return m
}

func startedAt(id ID) string {
	return id.Day + "T" + id.At[0:2] + ":" + id.At[2:4] + ":" + id.At[4:6]
}

// Filter narrows a listing. Everything is optional.
type Filter struct {
	CameraID string
	Day      string
	// Start and Limit page the result the way the firmware's own listings do,
	// so the frontend counts recordings the same way on both sides.
	Start int
	Limit int
}

// List returns held recordings newest first, with whether more were left.
//
// This walks the tree rather than consulting an index. A camera makes a few
// hundred recordings a day and the tree is one small directory read per day,
// which costs less than any index that could get out of step with what is
// actually stored.
func (s *Store) List(f Filter) ([]Recording, bool, error) {
	all, err := s.scan(f.CameraID, f.Day)
	if err != nil {
		return nil, false, err
	}
	sortNewestFirst(all)

	if f.Start > 0 {
		if f.Start >= len(all) {
			return []Recording{}, false, nil
		}
		all = all[f.Start:]
	}
	if f.Limit > 0 && len(all) > f.Limit {
		return all[:f.Limit], true, nil
	}
	return all, false, nil
}

// Days summarises what is held, newest day first.
func (s *Store) Days(cameraID string) ([]Day, error) {
	recs, err := s.scan(cameraID, "")
	if err != nil {
		return nil, err
	}
	index := make(map[string]*Day)
	var out []*Day
	for _, r := range recs {
		key := r.CameraID + "/" + r.Day
		d, ok := index[key]
		if !ok {
			d = &Day{CameraID: r.CameraID, Day: r.Day}
			index[key] = d
			out = append(out, d)
		}
		d.Recordings++
		d.Bytes += r.Bytes
	}
	days := make([]Day, 0, len(out))
	for _, d := range out {
		days = append(days, *d)
	}
	sort.Slice(days, func(i, j int) bool {
		if days[i].Day != days[j].Day {
			return days[i].Day > days[j].Day
		}
		return days[i].CameraID < days[j].CameraID
	})
	return days, nil
}

func sortNewestFirst(recs []Recording) {
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Day != recs[j].Day {
			return recs[i].Day > recs[j].Day
		}
		if recs[i].At != recs[j].At {
			return recs[i].At > recs[j].At
		}
		return recs[i].CameraID < recs[j].CameraID
	})
}

// scan reads the tree, optionally narrowed to one camera or one day.
func (s *Store) scan(cameraID, day string) ([]Recording, error) {
	cameras, err := subdirs(s.root, cameraID)
	if err != nil {
		return nil, err
	}
	var out []Recording
	for _, cam := range cameras {
		days, err := subdirs(filepath.Join(s.root, cam), day)
		if err != nil {
			return nil, err
		}
		for _, d := range days {
			if !ValidDay(d) {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(s.root, cam, d))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			// A transcode that was interrupted between renaming the MP4 in and
			// removing the AVI leaves both on the disk. That is one recording,
			// in the form that was verified, rather than two.
			seen := make(map[string]int, len(entries))
			for _, e := range entries {
				at, format, ok := recordingName(e)
				if !ok {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				id := ID{CameraID: cam, Day: d, At: at}
				meta := s.readMeta(id)
				rec := Recording{
					CameraID:  cam,
					Day:       d,
					At:        at,
					StartedAt: startedAt(id),
					DurMs:     meta.DurMs,
					Bytes:     info.Size(),
					Frames:    meta.Frames,
					Source:    meta.Source,
					Format:    format,
					HeldAt:    meta.HeldAt,
				}
				if i, dup := seen[at]; dup {
					if format == FormatMP4 {
						out[i] = rec
					}
					continue
				}
				seen[at] = len(out)
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

// subdirs lists a directory's subdirectories, or just the one named.
func subdirs(dir, only string) ([]string, error) {
	if only != "" {
		if strings.ContainsAny(only, `/\`) || only == "." || only == ".." {
			return nil, nil
		}
		if st, err := os.Stat(filepath.Join(dir, only)); err != nil || !st.IsDir() {
			return nil, nil
		}
		return []string{only}, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Usage measures what the archive is taking up, including writes in progress.
func (s *Store) Usage() (Usage, error) {
	u := Usage{MaxBytes: s.maxBytes}
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		u.Bytes += info.Size()
		switch {
		case strings.HasPrefix(d.Name(), tempPrefix):
			u.Pending += info.Size()
		case strings.HasSuffix(d.Name(), "."+FormatMP4):
			u.Recordings++
			u.Transcoded++
		case strings.HasSuffix(d.Name(), "."+FormatAVI):
			u.Recordings++
		}
		return nil
	})
	if err != nil {
		return u, err
	}
	return u, nil
}

// Sweep ages the archive down to its size limit, oldest recording first, and
// reports what it removed.
//
// A recording aged out is noted, so that a camera which still has it on its own
// card is not asked for it again on the next pass.
//
// Retention is by total size because that is the question a disk actually asks.
// Three rules hold it in: a recording still being written is never a candidate,
// because it is not under a name this can see; the newest recording is never
// removed, because an archive that empties itself is worse than one that is
// over its limit; and nothing on a camera's card is touched at all.
func (s *Store) Sweep() (removed int, freed int64, err error) {
	if s.maxBytes <= 0 {
		return 0, 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	usage, err := s.Usage()
	if err != nil {
		return 0, 0, err
	}
	if usage.Bytes <= s.maxBytes {
		return 0, 0, nil
	}

	recs, err := s.scan("", "")
	if err != nil {
		return 0, 0, err
	}
	sortNewestFirst(recs)

	// Oldest first, and never the last one standing.
	for i := len(recs) - 1; i > 0 && usage.Bytes > s.maxBytes; i-- {
		r := recs[i]
		id := r.ID()
		if err := os.Remove(s.File(id, r.Format)); err != nil {
			continue
		}
		freed += r.Bytes
		usage.Bytes -= r.Bytes
		removed++
		s.noteAged(id)
		if st, err := os.Stat(s.metaPath(id)); err == nil {
			if os.Remove(s.metaPath(id)) == nil {
				freed += st.Size()
				usage.Bytes -= st.Size()
			}
		}
	}
	s.pruneEmptyDays()
	return removed, freed, nil
}

// pruneEmptyDays removes day directories nothing is left in, so a year of
// aged-out days does not stay as a year of empty folders. Remove fails on a
// directory that still holds anything, including a write in progress, which is
// exactly the check wanted here.
func (s *Store) pruneEmptyDays() {
	cameras, err := subdirs(s.root, "")
	if err != nil {
		return
	}
	for _, cam := range cameras {
		days, err := subdirs(filepath.Join(s.root, cam), "")
		if err != nil {
			continue
		}
		for _, d := range days {
			os.Remove(filepath.Join(s.root, cam, d))
		}
	}
}

// Meta returns what the sidecar beside a recording says about it: the length
// and frame count the camera reported, which the AVI itself cannot state as
// precisely, and where the recording came from.
func (s *Store) Meta(id ID) (Meta, error) {
	if !id.valid() {
		return Meta{}, ErrNotFound
	}
	if !s.Has(id) {
		return Meta{}, ErrNotFound
	}
	return s.readMeta(id), nil
}
