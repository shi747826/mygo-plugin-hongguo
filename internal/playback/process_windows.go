package playback

import (
	"os/exec"
	"syscall"
)

func configureFFmpegCommand(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
