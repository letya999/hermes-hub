package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// CHG-0133 S6.5: `hubctl credentials migrate` re-anchors legacy local
// credential references onto Credential Broker for contract-bound
// definitions. Migration mints a broker request per reference (the owner
// submits through the reviewed link); a repeated pass anchors references
// whose requests resolved.
func runCredentials(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "migrate" {
		return fmt.Errorf("credentials requires migrate")
	}
	f := flag.NewFlagSet("credentials migrate", flag.ContinueOnError)
	toolHubStore := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "ToolHub registry path")
	rollback := f.Bool("rollback", false, "restore legacy locators on migrated references")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *toolHubStore == "" {
		return fmt.Errorf("credentials migrate requires --toolhub-store")
	}
	registry, err := toolhub.Load(*toolHubStore)
	if err != nil {
		return err
	}
	var report toolhub.MigrateReport
	if *rollback {
		report, err = toolhub.RollbackCredentials(ctx, registry)
	} else {
		config, cfgErr := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_CONTROL_")
		if cfgErr != nil {
			return cfgErr
		}
		if !config.Enabled() {
			return fmt.Errorf("credentials migrate requires HUB_CREDENTIAL_BROKER_CONTROL_URL")
		}
		report, err = toolhub.MigrateCredentials(ctx, registry, func(conn api.Connection) (toolhub.MigrateBroker, error) {
			auth := identity.Envelope{Schema: identity.Schema, PrincipalID: conn.Owner.ID, ExternalIdentityID: conn.Owner.ID, ContextID: conn.Owner.ID, RuntimeID: "hubctl", ConversationID: "hubctl", DeliveryTargetID: "hubctl", PolicyVersion: "hubctl"}
			return config.New(auth, "broker:control")
		})
	}
	if err != nil {
		return err
	}
	if err := registry.Save(*toolHubStore); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
