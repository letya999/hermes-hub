package app

import (
	"bytes"
	"errors"
	"path/filepath"

	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
)

// MigrateStore stages the ledger's SQLCipher import while the service is
// stopped. The caller activates only after the staged manifest verifies.
func MigrateStore(c Config) error {
	master, err := securefs.ReadKey(c.MasterKeyFile)
	if err != nil {
		return err
	}
	defer clear(master)
	if c.RecoveryDir == "" || c.RecoveryAnchorDir == "" {
		return journal.ImportSQLCipherSingle(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes)
	}
	if err := validateRecoveryLayout(c); err != nil {
		return err
	}
	raw, err := securefs.ReadKey(c.RecoveryKeyFile)
	if err != nil {
		return err
	}
	defer clear(raw)
	if bytes.Equal(master, raw) {
		return errors.New("recovery key must differ from master key")
	}
	recoveryKey := seal.Derive(raw, "identity/"+c.RecoveryIdentity)
	defer clear(recoveryKey)
	_, err = journal.ImportSQLCipher(c.DataDir, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, seal.Derive(master, "ledger"), seal.Derive(recoveryKey, "ledger"), c.MaxLedgerBytes)
	return err
}

// ActivateStore verifies the staged import and flips the store onto the
// encrypted databases; the framed ledgers retire into substrate-backup.
func ActivateStore(c Config) error {
	master, err := securefs.ReadKey(c.MasterKeyFile)
	if err != nil {
		return err
	}
	defer clear(master)
	if c.RecoveryDir == "" || c.RecoveryAnchorDir == "" {
		return journal.ActivateSQLCipherSingle(c.DataDir, seal.Derive(master, "ledger"))
	}
	if err := validateRecoveryLayout(c); err != nil {
		return err
	}
	raw, err := securefs.ReadKey(c.RecoveryKeyFile)
	if err != nil {
		return err
	}
	defer clear(raw)
	if bytes.Equal(master, raw) {
		return errors.New("recovery key must differ from master key")
	}
	recoveryKey := seal.Derive(raw, "identity/"+c.RecoveryIdentity)
	defer clear(recoveryKey)
	return journal.ActivateSQLCipher(c.DataDir, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, seal.Derive(master, "ledger"), seal.Derive(recoveryKey, "ledger"))
}

// RetainAudits runs one audit-retention pass on a stopped store: export the
// hot audit prefix into a verified sealed archive and, only when destructive
// is set, prune the verified source rows. The store's configured policy is
// used; a missing policy fails before anything is touched (SPEC-0087 R5).
func RetainAudits(c Config, destructive bool) (journal.RetainReport, error) {
	master, err := securefs.ReadKey(c.MasterKeyFile)
	if err != nil {
		return journal.RetainReport{}, err
	}
	defer clear(master)
	var j *journal.Journal
	if c.RecoveryDir == "" || c.RecoveryAnchorDir == "" {
		j, err = journal.Open(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes)
	} else {
		if err := validateRecoveryLayout(c); err != nil {
			return journal.RetainReport{}, err
		}
		raw, err := securefs.ReadKey(c.RecoveryKeyFile)
		if err != nil {
			return journal.RetainReport{}, err
		}
		defer clear(raw)
		if bytes.Equal(master, raw) {
			return journal.RetainReport{}, errors.New("recovery key must differ from master key")
		}
		recoveryKey := seal.Derive(raw, "identity/"+c.RecoveryIdentity)
		defer clear(recoveryKey)
		j, err = journal.OpenReplicatedAnchored(c.DataDir, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, seal.Derive(master, "ledger"), seal.Derive(recoveryKey, "ledger"), c.MaxLedgerBytes)
	}
	if err != nil {
		return journal.RetainReport{}, err
	}
	defer func() { _ = j.Close() }()
	j.SetAuditRetention(journal.AuditRetention{HotMaxBytes: c.AuditHotMaxBytes, ArchiveDir: c.AuditArchiveDir, Destructive: destructive})
	return j.RetainAudits()
}

// NormalizeStore backfills the derived current-state and audit tables on an
// activated store while the service is stopped. Until this runs the journal
// replays history on open; afterwards starts read keyed state rows.
func NormalizeStore(c Config) error {
	master, err := securefs.ReadKey(c.MasterKeyFile)
	if err != nil {
		return err
	}
	defer clear(master)
	if c.RecoveryDir == "" || c.RecoveryAnchorDir == "" {
		return journal.NormalizeSQLCipherSingle(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes, broker.NormalizeRecord)
	}
	if err := validateRecoveryLayout(c); err != nil {
		return err
	}
	raw, err := securefs.ReadKey(c.RecoveryKeyFile)
	if err != nil {
		return err
	}
	defer clear(raw)
	if bytes.Equal(master, raw) {
		return errors.New("recovery key must differ from master key")
	}
	recoveryKey := seal.Derive(raw, "identity/"+c.RecoveryIdentity)
	defer clear(recoveryKey)
	return journal.NormalizeSQLCipher(c.DataDir, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, seal.Derive(master, "ledger"), seal.Derive(recoveryKey, "ledger"), c.MaxLedgerBytes, broker.NormalizeRecord)
}
