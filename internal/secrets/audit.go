package secrets

import (
	"errors"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
)

type mutationAudit struct {
	operation *audit.Operation
	execution string
}

func mutationEvent(owner, kind, action string) audit.Event {
	event := audit.NewEvent(kind, owner, "admitted")
	event.Action, event.ContextID, event.CorrelationID = action, owner, "legacy-credentials"
	return event
}

func (s *Service) beginMutation(event audit.Event) (*mutationAudit, error) {
	operation, err := s.Audit.Begin(event)
	if err != nil {
		return nil, err
	}
	return &mutationAudit{operation: operation, execution: "not_started"}, nil
}

func (a *mutationAudit) finish(resultErr *error) {
	if a.execution == "unknown" && *resultErr != nil {
		*resultErr = audit.ErrUncertain
		return
	}
	execution, outcome := a.execution, "ok"
	if execution == "unknown" {
		execution = "succeeded"
	}
	if *resultErr != nil {
		outcome = "error"
	}
	if err := a.operation.Complete(execution, "not_applicable", outcome, ""); err != nil {
		*resultErr = err
	}
}

func (s *Service) degrade(owner, locator string, names []string) error {
	resultErr := s.Backend.SetStatus(locator, owner, credstore.StatusDegraded)
	if s.Registry != nil {
		for _, ref := range s.Registry.ConnectionsForSecret(locator, owner, names) {
			resultErr = errors.Join(resultErr, s.Registry.MarkDegraded(ref.ConnectionID))
		}
	}
	return resultErr
}
