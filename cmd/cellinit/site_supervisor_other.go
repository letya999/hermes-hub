//go:build !linux

package main

import (
	"context"
	"errors"
	"os/exec"
)

func protectSiteSupervisor() error { return errors.New("site supervisor requires Linux") }

func runSiteProcess(context.Context, *exec.Cmd) error {
	return errors.New("site supervisor requires Linux")
}
