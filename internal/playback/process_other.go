//go:build !windows

package playback

import "os/exec"

func configureFFmpegCommand(*exec.Cmd) {}
