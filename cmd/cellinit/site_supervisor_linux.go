package main

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"syscall"
)

// A site shares the UID of its trusted PID1. Non-dumpable prevents the child
// from modifying PID1 through /proc/1/mem even on a host with ptrace_scope=0.
func protectSiteSupervisor() error {
	if unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil {
		return errors.New("site supervisor protection unavailable")
	}
	return nil
}

// Wait4 has a single owner: racing exec.Cmd.Wait with a PID1 reaper can reap
// the main child twice. All relay/orphan exits are drained until the app exits.
func runSiteProcess(ctx context.Context, command *exec.Cmd) error {
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return errors.New("site output sink unavailable")
	}
	defer sink.Close()
	command.Stdout = sink
	command.Stderr = sink
	if command.Start() != nil {
		return errors.New("site process unavailable")
	}
	defer command.Process.Release()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
		case <-done:
		}
	}()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return errors.New("site supervision unavailable")
		}
		if pid == command.Process.Pid {
			if status.Exited() && status.ExitStatus() == 0 {
				return nil
			}
			return errors.New("site process stopped")
		}
	}
}
