package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/provider"
)

func recoveryPathsOverlap(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return true
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func validateRecoveryLayout(c Config) error {
	if c.RecoveryDir == "" || c.RecoveryAnchorDir == "" || c.RecoveryKeyFile == "" || c.RecoveryIdentity == "" {
		return errors.New("recovery configuration required")
	}
	if recoveryPathsOverlap(c.DataDir, c.RecoveryDir) || recoveryPathsOverlap(c.DataDir, c.RecoveryAnchorDir) || recoveryPathsOverlap(c.RecoveryDir, c.RecoveryAnchorDir) || recoveryPathsOverlap(c.DataDir, c.RecoveryKeyFile) || recoveryPathsOverlap(c.RecoveryDir, c.RecoveryKeyFile) || recoveryPathsOverlap(c.RecoveryAnchorDir, c.RecoveryKeyFile) {
		return errors.New("recovery stores must be separate")
	}
	return nil
}

// MigrateRecovery copies ledger-referenced local versions while holding the
// primary journal lock. The caller must keep Broker stopped until cutover.
func MigrateRecovery(c Config) (err error) {
	if err := validateRecoveryLayout(c); err != nil {
		return err
	}
	master, err := securefs.ReadKey(c.MasterKeyFile)
	if err != nil {
		return err
	}
	defer clear(master)
	rawRecoveryKey, err := securefs.ReadKey(c.RecoveryKeyFile)
	if err != nil {
		return err
	}
	defer clear(rawRecoveryKey)
	if bytes.Equal(master, rawRecoveryKey) {
		return errors.New("recovery key must differ from master key")
	}
	recoveryKey := seal.Derive(rawRecoveryKey, "identity/"+c.RecoveryIdentity)
	defer clear(recoveryKey)
	legacy := c
	legacy.RecoveryDir = ""
	legacy.RecoveryAnchorDir = ""
	legacy.RecoveryKeyFile = ""
	legacy.RecoveryIdentity = ""
	svc, err := Build(legacy)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, svc.Broker.Close()) }()
	providers, err := assembleProviders(c.Providers, master, c.RecoveryDir, recoveryKey)
	if err != nil {
		return err
	}
	for _, ref := range svc.Broker.RecoveryRefs() {
		if local, ok := providers[ref.Provider].(*provider.Local); ok {
			if err := local.BackupExisting(context.Background(), ref); err != nil {
				return err
			}
		}
	}
	return journal.SeedReplicatedFromOpen(svc.ledger, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, seal.Derive(recoveryKey, "ledger"), c.MaxLedgerBytes)
}
