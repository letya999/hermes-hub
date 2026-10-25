package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

// runImage is the operator rollout surface (issue 198): a pin only moves
// behind a passing smoke. The smoke gate is injected (devcheck.DockerSmoke
// in production, a stub in tests).
func runImage(ctx context.Context, out io.Writer, dir, env string, smoke func(context.Context, string) error, args []string) error {
	if len(args) == 0 {
		return errors.New("image requires a subcommand: status, promote, revert or canary")
	}
	switch args[0] {
	case "status":
		pins, err := supervisor.ReadRolloutPins(dir)
		if err != nil {
			return err
		}
		e := pins.Envs[env]
		fmt.Fprintf(out, "%s: current %s\n", env, e.Current.Image)
		if e.Previous != nil {
			fmt.Fprintf(out, "%s: previous %s\n", env, e.Previous.Image)
		}
		if e.Canary != nil {
			fmt.Fprintf(out, "%s: canary %s on context %s\n", env, e.Canary.Image, e.Canary.ContextID)
		}
		return nil
	case "revert":
		unlock, err := lockImagePins(dir)
		if err != nil {
			return err
		}
		defer unlock()
		pins, err := supervisor.ReadRolloutPins(dir)
		if err != nil {
			return err
		}
		e := pins.Envs[env]
		if e.Previous == nil {
			return fmt.Errorf("%s: no previous verified image to revert to", env)
		}
		target := *e.Previous
		if err := smoke(ctx, target.Image); err != nil {
			return err
		}
		e.Current = target
		e.Canary = nil
		pins.Envs[env] = e
		return supervisor.WriteRolloutPins(dir, pins)
	case "canary":
		f := flag.NewFlagSet("image canary", flag.ContinueOnError)
		image := f.String("image", "", "canary image reference")
		contextID := f.String("context", "", "context id to steer")
		clear := f.Bool("clear", false, "remove the canary")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		unlock, err := lockImagePins(dir)
		if err != nil {
			return err
		}
		defer unlock()
		pins, err := supervisor.ReadRolloutPins(dir)
		if err != nil {
			return err
		}
		e := pins.Envs[env]
		if *clear {
			e.Canary = nil
		} else {
			if *image == "" || *contextID == "" {
				return errors.New("canary requires --image and --context (or --clear)")
			}
			e.Canary = &supervisor.CanaryPin{Image: *image, ContextID: *contextID, SetAt: time.Now().UTC()}
		}
		pins.Envs[env] = e
		return supervisor.WriteRolloutPins(dir, pins)
	case "promote":
		f := flag.NewFlagSet("image promote", flag.ContinueOnError)
		image := f.String("image", "", "candidate image reference")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *image == "" {
			return errors.New("promote requires --image")
		}
		unlock, err := lockImagePins(dir)
		if err != nil {
			return err
		}
		defer unlock()
		if err := smoke(ctx, *image); err != nil {
			return fmt.Errorf("smoke gate failed; pin unchanged: %w", err)
		}
		pins, err := supervisor.ReadRolloutPins(dir)
		if err != nil {
			return err
		}
		e := pins.Envs[env]
		if e.Current.Image != "" {
			prev := e.Current
			e.Previous = &prev
		}
		e.Current = supervisor.RolloutPin{Image: *image, SmokedAt: time.Now().UTC()}
		pins.Envs[env] = e
		return supervisor.WriteRolloutPins(dir, pins)
	default:
		return fmt.Errorf("unknown image subcommand %q", args[0])
	}
}

// lockImagePins serializes promote/revert/canary mutations on one host so two
// operators cannot interleave a pin shift.
func lockImagePins(dir string) (func(), error) {
	lockDir := filepath.Join(dir, "generated")
	if err := os.MkdirAll(lockDir, 0o700); err != nil { // #nosec G703 -- operator-supplied --dir, fixed subdir
		return nil, err
	}
	lock := flock.New(filepath.Join(lockDir, ".image-pins.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		_ = lock.Close()
		return nil, errors.New("image pins have an active writer")
	}
	return func() { _ = lock.Close() }, nil
}
