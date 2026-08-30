package camera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxConfigBody = 64 << 10
	maxReplyBody  = 4 << 10

	// maxReplyNote bounds what is worth quoting from a reply. The refusal list
	// is a few control names; anything longer is a page, not a message.
	maxReplyNote = 120
)

// A checkbox that is left out of a form post reads as unticked rather than
// unchanged, and the firmware's form handlers take a whole form. So every
// partial update starts from /config and rewrites the complete form, and these
// lists are what "complete" means for each handler.
type fieldKind int

const (
	kindInt fieldKind = iota
	kindCheckbox
)

type field struct {
	name string
	kind fieldKind
}

// imageFields is POST /image in full. aelnow, gcnow and unsupported are what
// the sensor reports rather than what is stored, so they are never posted back.
var imageFields = []field{
	{"autoimg", kindCheckbox},
	{"ael", kindInt},
	{"gc", kindInt},
	{"bri", kindInt},
	{"con", kindInt},
	{"sat", kindInt},
	{"wb", kindInt},
	{"gray", kindCheckbox},
	{"hmir", kindCheckbox},
	{"vflip", kindCheckbox},
	{"flashlvl", kindInt},
}

// recordingFields is POST /recording in full apart from the schedule days,
// which are a bitmask in /config and repeated fields in the form and so are
// handled separately.
var recordingFields = []field{
	{"moten", kindCheckbox},
	{"motsens", kindInt},
	{"recsec", kindInt},
	{"presec", kindInt},
	{"quietsec", kindInt},
	{"keepfree", kindInt},
	{"fsize", kindInt},
	{"jq", kindInt},
	{"schen", kindCheckbox},
	{"schfrom", kindInt},
	{"schto", kindInt},
}

// daysInWeek bounds the schedule bitmask: /config reports schdays as a bitmask
// of the same 0 to 6 days the form repeats as schday.
const daysInWeek = 7

// Settings is a partial update. Each map holds only the fields the caller means
// to change, keyed by the firmware's own names, with the values still as raw
// JSON so a number keeps the shape it was sent in.
type Settings struct {
	Image     map[string]json.RawMessage
	Recording map[string]json.RawMessage
}

// Empty reports whether there is nothing to apply.
func (s Settings) Empty() bool { return len(s.Image) == 0 && len(s.Recording) == 0 }

// Config returns the camera's whole /config document unparsed, so a field the
// firmware gains reaches the caller without this service knowing about it.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.Get(ctx, "/config")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxConfigBody))
		return nil, fmt.Errorf("/config: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBody))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("/config: response was not JSON")
	}
	return json.RawMessage(body), nil
}

// ApplySettings merges a partial update over the camera's current settings and
// posts the complete forms back. Reading /config first is what stops a change
// to one field from resetting every field the caller did not mention.
func (c *Client) ApplySettings(ctx context.Context, s Settings) error {
	if s.Empty() {
		return nil
	}

	raw, err := c.Config(ctx)
	if err != nil {
		return err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("/config: %w", err)
	}

	if len(s.Image) > 0 {
		form, err := BuildImageForm(cfg, s.Image)
		if err != nil {
			return err
		}
		reply, err := c.postSettings(ctx, "/image", form)
		if err != nil {
			return err
		}
		// /image answers "ok", or the list of controls this sensor refused.
		// A refusal is what the hardware can do rather than a failed request,
		// so it is worth a line in the log and nothing more; /config reports
		// the same list in its unsupported field.
		if reply != "" && !strings.EqualFold(reply, "ok") {
			log.Printf("camera %s: image settings applied, sensor refused %s", c.cam.Name, reply)
		}
	}
	if len(s.Recording) > 0 {
		form, err := BuildRecordingForm(cfg, s.Recording)
		if err != nil {
			return err
		}
		// /recording answers with its own settings page, which is not worth
		// reading, so only the status matters here.
		if _, err := c.postSettings(ctx, "/recording", form); err != nil {
			return err
		}
	}
	return nil
}

// postSettings sends one whole form and returns the camera's reply, trimmed and
// bounded: some routes answer in a word and others with an entire HTML page.
func (c *Client) postSettings(ctx context.Context, path string, form url.Values) (string, error) {
	resp, err := c.PostForm(ctx, path, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	reply := strings.TrimSpace(string(body))
	if len(reply) > maxReplyNote || strings.HasPrefix(reply, "<") {
		return "", nil
	}
	return reply, nil
}

// BuildImageForm renders the whole POST /image form from the camera's current
// config with patch laid over it.
func BuildImageForm(cfg, patch map[string]json.RawMessage) (url.Values, error) {
	return buildForm(imageFields, cfg, patch, nil)
}

// BuildRecordingForm renders the whole POST /recording form, translating the
// schdays bitmask into one repeated schday field per enabled day.
func BuildRecordingForm(cfg, patch map[string]json.RawMessage) (url.Values, error) {
	form, err := buildForm(recordingFields, cfg, patch, scheduleKeys)
	if err != nil {
		return nil, err
	}
	days, err := scheduleDays(cfg, patch)
	if err != nil {
		return nil, err
	}
	for _, d := range days {
		form.Add("schday", strconv.Itoa(d))
	}
	return form, nil
}

// scheduleKeys are handled by scheduleDays, so buildForm must not also pass
// them through as unrecognised fields.
var scheduleKeys = map[string]bool{"schdays": true, "schday": true}

// buildForm lays patch over cfg and renders every field of the form. A field
// missing from both is left out, because a value this service never saw is one
// it cannot honestly send. Fields the caller names that are not in the list are
// passed through as they came: the firmware ignores what it does not accept, so
// a field a later firmware adds still reaches it.
func buildForm(fields []field, cfg, patch map[string]json.RawMessage, skip map[string]bool) (url.Values, error) {
	form := url.Values{}
	known := make(map[string]bool, len(fields))

	for _, f := range fields {
		known[f.name] = true
		raw, ok := patch[f.name]
		if !ok {
			raw, ok = cfg[f.name]
		}
		if !ok {
			continue
		}
		switch f.kind {
		case kindCheckbox:
			on, err := asBool(f.name, raw)
			if err != nil {
				return nil, err
			}
			// An unticked checkbox is an absent field. There is no value that
			// means off.
			if on {
				form.Set(f.name, "1")
			}
		default:
			n, err := asInt(f.name, raw)
			if err != nil {
				return nil, err
			}
			form.Set(f.name, strconv.Itoa(n))
		}
	}

	for name, raw := range patch {
		if known[name] || skip[name] {
			continue
		}
		v, ok, err := literal(name, raw)
		if err != nil {
			return nil, err
		}
		if ok {
			form.Set(name, v)
		}
	}
	return form, nil
}

// scheduleDays works out which days the schedule covers, accepting the bitmask
// /config reports and, for callers that would rather be explicit, a list of
// days. The result is sorted and free of duplicates so the form is stable.
func scheduleDays(cfg, patch map[string]json.RawMessage) ([]int, error) {
	for _, key := range []string{"schday", "schdays"} {
		if raw, ok := patch[key]; ok {
			return parseDays(key, raw)
		}
	}
	if raw, ok := cfg["schdays"]; ok {
		return parseDays("schdays", raw)
	}
	return nil, nil
}

func parseDays(name string, raw json.RawMessage) ([]int, error) {
	if list, ok := jsonArray(raw); ok {
		seen := make(map[int]bool, daysInWeek)
		for _, item := range list {
			d, err := asInt(name, item)
			if err != nil {
				return nil, err
			}
			if d < 0 || d >= daysInWeek {
				return nil, fmt.Errorf("field %q: day %d is outside 0 to 6", name, d)
			}
			seen[d] = true
		}
		days := make([]int, 0, len(seen))
		for d := 0; d < daysInWeek; d++ {
			if seen[d] {
				days = append(days, d)
			}
		}
		return days, nil
	}

	mask, err := asInt(name, raw)
	if err != nil {
		return nil, err
	}
	days := make([]int, 0, daysInWeek)
	for d := 0; d < daysInWeek; d++ {
		if mask&(1<<d) != 0 {
			days = append(days, d)
		}
	}
	return days, nil
}

func jsonArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw))[0] != '[' {
		return nil, false
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	return list, true
}

// asBool reads a checkbox value. JSON true is the obvious form, but a UI that
// round-trips the firmware's own 1 and 0 should not be punished for it.
func asBool(name string, raw json.RawMessage) (bool, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("field %q: %w", name, err)
	}
	switch t := v.(type) {
	case bool:
		return t, nil
	case float64:
		return t != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "on", "yes":
			return true, nil
		case "", "0", "false", "off", "no":
			return false, nil
		}
	}
	return false, fmt.Errorf("field %q: %s is not on or off", name, raw)
}

func asInt(name string, raw json.RawMessage) (int, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("field %q: %w", name, err)
	}
	switch t := v.(type) {
	case float64:
		if t != float64(int(t)) {
			return 0, fmt.Errorf("field %q: %v is not a whole number", name, t)
		}
		return int(t), nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("field %q: %q is not a whole number", name, t)
		}
		return n, nil
	}
	return 0, fmt.Errorf("field %q: %s is not a whole number", name, raw)
}

// literal renders a field this service does not know, by the shape of its JSON.
// A false boolean is an unticked checkbox, which is an absent field.
func literal(name string, raw json.RawMessage) (string, bool, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false, fmt.Errorf("field %q: %w", name, err)
	}
	switch t := v.(type) {
	case bool:
		if !t {
			return "", false, nil
		}
		return "1", true, nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true, nil
	case string:
		return t, true, nil
	}
	return "", false, fmt.Errorf("field %q: %s cannot be sent as a form value", name, raw)
}
