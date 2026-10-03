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

func HermesCapabilityContract(context.Context, string) error {
	return errors.New("hermes capability probe requires: go run -tags integration ./cmd/devcheck hermes-capability-contract IMAGE")
}

func ManagedNetworkCanary(context.Context, string) error {
	return errors.New("managed network canary requires: go run -tags integration ./cmd/devcheck managed-network-canary IMAGE")
}

func ManagedSupervisorCanary(context.Context, string) error {
	return errors.New("managed supervisor canary requires: go run -tags integration ./cmd/devcheck managed-supervisor-canary IMAGE")
}

func GatewayLifecycle(context.Context, string) error {
	return errors.New("gateway lifecycle probe requires: go run -tags integration ./cmd/devcheck gateway-lifecycle IMAGE")
}

func ToolHubHermesContract(context.Context) error {
	return errors.New("ToolHub Hermes probe requires: go run -tags integration ./cmd/devcheck toolhub-hermes-contract")
}
