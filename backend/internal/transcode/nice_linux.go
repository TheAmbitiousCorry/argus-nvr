//go:build linux

package transcode

import (
	"os/exec"
	"syscall"
)

// niceness is how far the encoder is pushed behind everything else. Ten is
// enough that a transcode gets only what the live view is not using, and not so
// far that a quiet machine takes all afternoon over a backlog.
const niceness = 10

// prepare puts the encoder in a process group of its own, so its priority can
// be lowered without touching the service's.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// lower drops the priority of a started encoder.
//
// It runs after the fork rather than being inherited through it, because Go's
// exec has nothing that sets a priority before the child runs. The window is
// the microseconds between starting and this call, on a process that runs for a
// second or more, and it costs no shell in an image that has none: nice here is
// a system call rather than the binary of the same name.
func lower(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, niceness)
}
