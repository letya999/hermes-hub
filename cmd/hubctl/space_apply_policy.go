package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/passport"
)

func applyPolicyPublisher() (contract.PassportPublisher, error) {
	token := strings.TrimSpace(os.Getenv("HUB_SUPERVISOR_ADMIN_AUTH"))
	if token == "" {
		return nil, nil
	}
	endpoint := strings.TrimSpace(os.Getenv("HUB_PASSPORT_PUBLISHER_URL"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8876"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, contract.ErrInvalid
	}
	httpClient := &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return contract.NewPassportPublisherHTTPClient(endpoint, token, httpClient), nil
}

func stageApplyPolicy(ctx context.Context, dir string, rev passport.Revision, record *applyJournal) (contract.PassportPublisher, error) {
	cached, err := passport.RevisionPolicyDigest(dir, record.Previous)
	if err != nil {
		return nil, err
	}
	publisher, err := applyPolicyPublisher()
	if err != nil {
		return nil, err
	}
	if publisher == nil {
		if cached != "" {
			return nil, contract.ErrForbidden
		}
		return nil, nil
	}
	active, err := publisher.ActivePassports(ctx, filepath.Base(dir), record.Environment)
	if err != nil && !errors.Is(err, contract.ErrNotFound) {
		return nil, err
	}
	if active != cached {
		return nil, contract.ErrConflict
	}
	bundle, err := rev.PolicyBundle()
	if err != nil {
		return nil, err
	}
	if bundle.Space != filepath.Base(dir) || bundle.Environment != record.Environment {
		return nil, contract.ErrInvalid
	}
	staged, err := publisher.StagePassports(ctx, bundle)
	if err != nil {
		return nil, err
	}
	record.PreviousPolicyDigest, record.PolicyDigest = active, staged.Digest
	return publisher, nil
}

func restoreApplyPolicy(ctx context.Context, dir string, record applyJournal) error {
	if record.PolicyDigest == "" {
		return nil
	}
	publisher, err := applyPolicyPublisher()
	if err != nil {
		return err
	}
	if publisher == nil {
		return contract.ErrForbidden
	}
	space := filepath.Base(dir)
	active, err := publisher.ActivePassports(ctx, space, record.Environment)
	if err != nil && !errors.Is(err, contract.ErrNotFound) {
		return err
	}
	if active == record.PreviousPolicyDigest {
		return nil
	}
	if active != record.PolicyDigest {
		return contract.ErrConflict
	}
	return publisher.ActivatePassports(ctx, space, record.Environment, active, record.PreviousPolicyDigest)
}
