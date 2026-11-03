package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
)

// `hubctl models` hosts model-service operator flows. `models login` enrolls
// a cliproxy service's session and client key into Credential Broker.
func runModels(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("models login", flag.ContinueOnError)
	dir := f.String("dir", "", "space directory")
	service := f.String("service", "", "models.yaml service name")
	rest := args
	if len(rest) > 0 && rest[0] == "login" {
		rest = rest[1:]
	}
	if err := f.Parse(rest); err != nil {
		return err
	}
	_ = service
	if *dir == "" {
		return fmt.Errorf("models login requires --dir")
	}
	config, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_CONTROL_")
	if err != nil {
		return err
	}
	if !config.Enabled() {
		return fmt.Errorf("models login requires HUB_CREDENTIAL_BROKER_CONTROL_URL")
	}
	// The grant pins the grantor's identity: the init companion acquires as
	// {space, space, space, cliproxy-materialize}, so the operator envelope must
	// present exactly that — a broker "operator" context can never own or grant
	// the space's credentials (grants.go: in.ContextID == credential.ContextID).
	eff, err := passportview.LoadEffective(*dir)
	if err != nil {
		return fmt.Errorf("models login: %w", err)
	}
	owner := eff.Header.Metadata.Space
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: owner, ExternalIdentityID: owner, ContextID: owner, RuntimeID: owner, ConversationID: "hubctl", DeliveryTargetID: "hubctl", PolicyVersion: "cliproxy-materialize"}
	broker, err := config.New(auth, "broker:control")
	if err != nil {
		return err
	}
	// The proxy_access key is also consumed by model-relay; its grant must be
	// minted under the relay's policy version so the relay's lease matches.
	relayAuth := auth
	relayAuth.PolicyVersion = "model-relay"
	relayBroker, err := config.New(relayAuth, "broker:control")
	if err != nil {
		return err
	}
	report, err := modelsLogin(ctx, *dir, *service, broker, relayBroker)
	if err != nil {
		return err
	}
	return printModelsReport(os.Stdout, report)
}

// printModelsReport emits the enrollment outcome; pending requests carry
// their reviewed link back to the operator.
func printModelsReport(w io.Writer, report modelsLoginReport) error {
	return json.NewEncoder(w).Encode(report)
}
