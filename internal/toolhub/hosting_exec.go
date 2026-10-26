package toolhub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type siteOutput struct {
	data bytes.Buffer
	max  int
}

func (b *siteOutput) Write(p []byte) (int, error) {
	if len(p) > b.max-b.data.Len() {
		n := max(0, b.max-b.data.Len())
		if n > 0 {
			_, _ = b.data.Write(p[:n])
		}
		return n, errors.New("site relay output limit")
	}
	return b.data.Write(p)
}
func siteCommandInput(ctx context.Context, input io.Reader, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- fixed Docker argv from the admitted site controller, never a model command.
	command.Env = api.IsolatedToolHiveEnviron(nil)
	command.Stdin = input
	output := &siteOutput{max: 12 << 20}
	command.Stdout = output
	command.Stderr = io.Discard
	if command.Run() != nil {
		return nil, errors.New("site relay command unavailable")
	}
	return output.data.Bytes(), nil
}
func (c *genericController) siteDockerInput(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	run := c.siteInput
	if run == nil {
		run = c.commandStdin
	}
	if run == nil {
		run = siteCommandInput
	}
	if c.scope == nil {
		return run(ctx, input, "docker", args...)
	}
	return c.scope.RunScoped(ctx, func(ctx context.Context, binary string, scoped ...string) ([]byte, error) {
		return run(ctx, input, binary, scoped...)
	}, "docker", args...)
}
