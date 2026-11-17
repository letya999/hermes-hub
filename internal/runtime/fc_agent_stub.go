//go:build !linux

package runtime

import (
	"context"
	"errors"
)

func runFCAgent(_ context.Context) error {
	return errors.New("fc-agent requires a Linux guest")
}
