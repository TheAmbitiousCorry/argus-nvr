package camera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Flash writes a firmware image to the camera. The bytes go up as the raw
// request body with Content-Length set, which is the route the firmware's own
// web updater uses and is about eight times faster than the espota path. The
// camera answers 200 and then reboots, so the session this used is already gone
// by the time the call returns.
func (c *Client) Flash(ctx context.Context, image []byte) error {
	if len(image) == 0 {
		return errors.New("firmware image is empty")
	}

	resp, err := c.PostRaw(ctx, "/update", "application/octet-stream", image)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if resp.StatusCode != http.StatusOK {
		if reply := strings.TrimSpace(string(body)); reply != "" {
			return fmt.Errorf("/update: status %d: %s", resp.StatusCode, reply)
		}
		return fmt.Errorf("/update: status %d", resp.StatusCode)
	}
	return nil
}

// maxVersionBody bounds /version. The document is five short fields, so this is
// generous and still keeps a camera answering with something unexpected from
// being read into memory whole.
const maxVersionBody = 4 << 10

// Version returns the camera's own /version document unparsed, so a field a
// later firmware gains reaches the caller without this service knowing about
// it. It is the same passing-through /config and /record already do.
func (c *Client) Version(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.Get(ctx, "/version")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxVersionBody))
		return nil, fmt.Errorf("/version: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVersionBody))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("/version: response was not JSON")
	}
	return json.RawMessage(body), nil
}
