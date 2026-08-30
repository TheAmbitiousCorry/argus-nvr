package camera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxListingBody bounds a day listing. A busy day is a few hundred entries of
// about sixty bytes, so this is generous and still keeps a camera answering
// with something unexpected from being read into memory whole.
const maxListingBody = 1 << 20

// ErrNoListing means this firmware has no JSON recordings listing. It is not a
// fault: the endpoints are newer than some of the cameras they run on, and a
// camera without them simply cannot be pulled from until it is updated.
var ErrNoListing = errors.New("camera has no recordings listing")

// Recording is one entry in a camera's day listing, named as the design
// document names them so the shape can be read straight off the page.
type Recording struct {
	At     string `json:"at"`     // HHMMSS, the directory the recording lives in
	DurMs  int64  `json:"durMs"`  // how long it ran
	Bytes  int64  `json:"bytes"`  // the size of the frames on the card
	Frames int    `json:"frames"` // how many frames it holds
}

// maxDayPages bounds the paging loop. A camera that always says there is more
// would otherwise be asked forever; twenty pages is years of days.
const maxDayPages = 20

// Days lists every day the camera holds recordings for, following the `more`
// flag until the camera has nothing left to add.
//
// A camera also holds recordings made before its clock reached an NTP server,
// which have a number instead of a date. They are skipped: a recording is
// identified here by camera, day and start time, and something with no day
// cannot be stored under that scheme or asked for again later.
func (c *Client) Days(ctx context.Context) ([]string, error) {
	var all []string
	for page, start := 0, 0; page < maxDayPages; page++ {
		days, more, err := c.daysPage(ctx, start)
		if err != nil {
			return nil, err
		}
		all = append(all, days...)
		if !more || len(days) == 0 {
			return all, nil
		}
		start += len(days)
	}
	return all, nil
}

func (c *Client) daysPage(ctx context.Context, start int) ([]string, bool, error) {
	path := "/recordings/days"
	if start > 0 {
		path += "?" + url.Values{"start": {strconv.Itoa(start)}}.Encode()
	}
	body, err := c.getJSON(ctx, path)
	if err != nil {
		return nil, false, err
	}

	var wrapped struct {
		Days json.RawMessage `json:"days"`
		More bool            `json:"more"`
	}
	haveWrapper := json.Unmarshal(body, &wrapped) == nil
	days, err := readDays(body, wrapped.Days, haveWrapper)
	if err != nil {
		return nil, false, err
	}
	return days, wrapped.More, nil
}

// readDays takes the three obvious ways to write the answer: a bare array of
// days, an object with a days field, and either of those holding objects with a
// day field rather than plain strings. The document says what the answer holds
// without pinning the punctuation, and being strict here would mean the two
// halves of this feature meeting and disagreeing over a bracket.
func readDays(body, wrapped json.RawMessage, haveWrapper bool) ([]string, error) {
	list := body
	if haveWrapper && len(wrapped) > 0 {
		list = wrapped
	}

	var plain []string
	if err := json.Unmarshal(list, &plain); err == nil {
		return validDays(plain), nil
	}
	var objects []struct {
		Day string `json:"day"`
	}
	if err := json.Unmarshal(list, &objects); err == nil {
		days := make([]string, 0, len(objects))
		for _, o := range objects {
			days = append(days, o.Day)
		}
		return validDays(days), nil
	}
	return nil, errors.New("/recordings/days: could not read the list of days")
}

// validDays drops anything that is not a date. A day becomes a directory name
// on this side, so what a camera says it has is checked rather than trusted.
func validDays(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		if len(d) == 10 && d[4] == '-' && d[7] == '-' && allDigits(d[0:4]) && allDigits(d[5:7]) && allDigits(d[8:10]) {
			out = append(out, d)
		}
	}
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// Recordings lists one day, starting at the offset given. The camera says
// whether there are more, in the same `more` field and with the same `start`
// paging the firmware's own file listing already uses.
func (c *Client) Recordings(ctx context.Context, day string, start int) (recs []Recording, more bool, err error) {
	q := url.Values{"day": {day}}
	if start > 0 {
		q.Set("start", strconv.Itoa(start))
	}
	body, err := c.getJSON(ctx, "/recordings?"+q.Encode())
	if err != nil {
		return nil, false, err
	}

	var doc struct {
		Recordings []Recording `json:"recordings"`
		More       bool        `json:"more"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, false, fmt.Errorf("/recordings: %w", err)
	}
	return doc.Recordings, doc.More, nil
}

// getJSON reads a bounded JSON body from the camera, turning a missing route
// into ErrNoListing so a caller can tell an old firmware from a broken one.
func (c *Client) getJSON(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBody))
		return nil, ErrNoListing
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBody))
		return nil, fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListingBody))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// OpenRecording starts the download of one recording as a ready-made AVI. The
// caller owns the body and must close it.
//
// This is the endpoint the camera already has. It reads the frames off the card
// and wraps them, so nothing is decoded on either side and the camera does no
// more work than it does to serve a plain file.
func (c *Client) OpenRecording(ctx context.Context, day, at string) (*http.Response, error) {
	q := url.Values{"dir": {"/rec/" + day + "/" + at}}
	resp, err := c.GetLarge(ctx, "/video?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		note, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyNote))
		resp.Body.Close()
		return nil, fmt.Errorf("/video: status %d%s", resp.StatusCode, quoted(string(note)))
	}
	return resp, nil
}

func quoted(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	return ": " + note
}
