package camera

import (
	"context"
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
