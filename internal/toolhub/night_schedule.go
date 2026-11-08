package toolhub

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// NightSchedule reads the current published per-user clock. It carries no
// model authority and is never sourced from ordinary routine arguments.
func (e *projectionEndpoint) NightSchedule(auth identity.Envelope) (night.Schedule, error) {
	enrolled, err := e.EnrolledAuth(auth.PrincipalID)
	if err != nil || !enrolled.Equal(auth) || auth.Environment == "" {
		return night.Schedule{}, api.ErrUnauthorized
	}
	g := e.gateway
	if g.PassportRoot == "" {
		return night.Schedule{}, errors.New("night passport unavailable")
	}
	activation, err := g.requireActivated(auth)
	if err != nil || activation.revision == "" {
		return night.Schedule{}, api.ErrUnauthorized
	}
	eff, err := passportview.LoadEffectiveGenerated(filepath.Join(g.PassportRoot, auth.PrincipalID))
	if err != nil || eff.Header.Metadata.Revision != activation.revision || eff.Header.Metadata.Space != auth.PrincipalID || eff.Header.Metadata.Environment != auth.Environment {
		return night.Schedule{}, api.ErrUnauthorized
	}
	plan := night.Schedule{
		Enabled:       eff.Config.Memory.Enabled != nil && *eff.Config.Memory.Enabled,
		LocalTime:     eff.Config.Memory.NightlyRollup,
		Timezone:      eff.Config.Runtime.Timezone,
		Revision:      eff.Header.Metadata.Revision,
		RetentionDays: eff.Config.Memory.LeafRetentionDays,
		FoldBytes:     eff.Config.Memory.LeafFoldBytes,
	}
	if plan.LocalTime == "" {
		plan.LocalTime = "04:00"
	}
	if plan.Timezone == "" {
		plan.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(plan.Timezone); err != nil {
		return night.Schedule{}, errors.New("night timezone unavailable")
	}
	if err := g.checkActivePassport(auth, activation.revision); err != nil {
		return night.Schedule{}, err
	}
	return plan, nil
}
