//go:build !linux

package transcode

import "os/exec"

// The service ships in a Linux image, and lowering a process's priority is
// where the platforms differ most. These exist so the package still builds on
// a developer's machine; there, an encode simply runs at the usual priority.
func prepare(cmd *exec.Cmd) {}

func lower(cmd *exec.Cmd) {}
