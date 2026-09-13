//go:build !integration

package devcheck

import (
	"context"
	"errors"
)

// HermesContract is built only for Docker-backed integration checks.
func HermesContract(context.Context, string) error {
	return errors.New("hermes contract probe requires: go run -tags integration ./cmd/devcheck hermes-contract IMAGE")
}

func GatewayLifecycle(context.Context, string) error {
	return errors.New("gateway lifecycle probe requires: go run -tags integration ./cmd/devcheck gateway-lifecycle IMAGE")
}

func ToolHubHermesContract(context.Context) error {
	return errors.New("ToolHub Hermes probe requires: go run -tags integration ./cmd/devcheck toolhub-hermes-contract")
}
