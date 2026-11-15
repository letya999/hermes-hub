package sshcap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/letya999/hermes-hub/internal/audit"
)

type sshAudit struct {
	operation *audit.Operation
	execution string
	receipt   string
}

func (s *Service) beginAudit(action, alias, target string) (*sshAudit, error) {
	event := audit.NewEvent("ssh", s.auth.PrincipalID, "admitted")
	event.Action = action
	switch action {
	case "ssh.shell_open":
		event.CleanupAction = "ssh.shell_close"
	case "ssh.tunnel_open":
		event.CleanupAction = "ssh.tunnel_close"
	}

	event.Name = alias
	event.ContextID = s.auth.ContextID
	event.RuntimeID = s.auth.RuntimeID
	event.PolicyRevision = s.auth.PolicyVersion
	event.Environment = s.auth.Environment
	event.Generation = s.auth.Generation
	digest := sha256.Sum256([]byte(action + "\x00" + target))
	event.CorrelationID = "ssh-" + hex.EncodeToString(digest[:])
	operation, err := s.ledger.Begin(event)
	if err != nil {
		return nil, err
	}
	return &sshAudit{operation: operation, execution: "not_started"}, nil
}

func (a *sshAudit) finish(resultErr *error) {
	if a.execution == "unknown" {
		// Preserve the durable intent/reserve: a transport failure proves no outcome.
		*resultErr = audit.ErrUncertain
		return
	}
	outcome := "ok"
	if *resultErr != nil || a.execution == "failed" {
		outcome = "error"
	}
	if err := a.operation.Complete(a.execution, "not_applicable", outcome, a.receipt); err != nil {
		*resultErr = errors.Join(audit.ErrUncertain, err)
	}
}
