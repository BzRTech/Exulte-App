//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

const ffmpegName = "ffmpeg"

var errUnsupportedOS = errors.New("Media Foundation indisponível")

func mediaFoundationToWav(src, dst string) error { return errUnsupportedOS }

func hideWindow(cmd *exec.Cmd) {}
