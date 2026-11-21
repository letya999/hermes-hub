package runtime

import (
	"context"
	"errors"
)

// Run dispatches the relay-side modes of the v0 runtime binary. The Hermes
// workload modes live in the harness-runtime service (M40.4); hub-runtime
// keeps only the relay processes until M40.12/M40.13 extract them.
func Run(args []string) error {
	if len(args) != 1 {
		return errors.New("expected model-relay, toolhub-relay, control-relay, egress-relay or cliproxy-materialize")
	}
	switch args[0] {
	case "model-relay":
		return runModelRelay(context.Background())
	case "toolhub-relay":
		return runToolHubRelay(context.Background())
	case "control-relay":
		return runControlRelay(context.Background())
	case "egress-relay":
		return runEgressRelay()
	case "cliproxy-materialize":
		return runCliproxyMaterialize(context.Background())
	default:
		return errors.New("expected model-relay, toolhub-relay, control-relay, egress-relay or cliproxy-materialize")
	}
}
