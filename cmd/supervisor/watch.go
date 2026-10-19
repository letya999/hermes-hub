package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"
)

func watchSupervisor(ctx context.Context, executable string, args []string) error {
	for ctx.Err() == nil {
		child := exec.CommandContext(ctx, executable, args...)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		err := child.Run()
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("supervisor child exited; restarting in 1s: %v", err)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
