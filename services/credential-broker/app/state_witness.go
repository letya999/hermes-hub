package app

import (
	"errors"
	"os"

	"github.com/letya999/credential-broker/internal/securefs"
)

var ErrStateRecoveryRequired = errors.New("broker state recovery required")

const stateWitnessVersion = "credential-broker-state/v1\n"

// This marker detects empty-state replacement, not rollback to a valid prefix.
func readStateWitness(dir string) (bool, error) {
	if err := securefs.Dir(dir); err != nil {
		return false, ErrStateRecoveryRequired
	}
	data, err := securefs.Read(dir, "state-initialized", 128)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || string(data) != stateWitnessVersion {
		return false, ErrStateRecoveryRequired
	}
	return true, nil
}
