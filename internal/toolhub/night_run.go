package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/memory/prune"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// NightRun executes the protected occurrence in its enrolled owner's vault.
func (e *projectionEndpoint) NightRun(ctx context.Context, auth identity.Envelope, leafDate string, plan night.Schedule, lang locale.Lang, sink func(context.Context, night.IntentProposal) error) (string, error) {
	if !identity.ValidID(auth.PrincipalID) || e.gateway.PassportRoot == "" {
		return "", api.ErrUnauthorized
	}
	digest, err := passportview.CurrentPolicyDigestGenerated(filepath.Join(e.gateway.PassportRoot, auth.PrincipalID))
	if err != nil {
		return "", err
	}
	issuerURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
	bootstrap := strings.TrimSpace(os.Getenv(contract.DreamingServiceAuthEnv))
	if issuerURL == "" || bootstrap == "" {
		return "", errors.New("night relay unavailable")
	}
	signer := contract.NewDreamingModelSigner(issuerURL, bootstrap)
	scope := contract.ServiceRequest{Space: auth.PrincipalID, Environment: auth.Environment, PolicyRevision: digest}
	url := "http://model-relay-" + auth.PrincipalID + ":8318"
	return e.nightRunWithRelay(ctx, auth, leafDate, plan, lang, sink, func(ctx context.Context, system, user string) (string, error) {
		return callNightRelay(ctx, url, signer, scope, system, user)
	})
}

func (e *projectionEndpoint) nightRunWithRelay(ctx context.Context, auth identity.Envelope, leafDate string, plan night.Schedule, lang locale.Lang, sink func(context.Context, night.IntentProposal) error, relay night.RelayFunc) (string, error) {
	if e.gateway.Vaults == nil || relay == nil {
		return "", api.ErrUnauthorized
	}
	check := func() error {
		current, err := e.NightSchedule(auth)
		if err != nil || !current.Enabled || current.Revision != plan.Revision {
			return api.ErrUnauthorized
		}
		decision, err := e.gateway.Store.Decide(auth, "memory", "dreaming", "", time.Now())
		if err != nil || !decision.Allow {
			return api.ErrUnauthorized
		}
		return nil
	}
	if err := check(); err != nil {
		return "", err
	}
	loc, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return "", api.ErrUnauthorized
	}
	day, err := time.ParseInLocation("2006-01-02", leafDate, loc)
	if err != nil || day.Format("2006-01-02") != leafDate {
		return "", api.ErrInvalid
	}
	v, err := e.gateway.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		return "", err
	}
	var digest string
	err = v.Locked(func(root string) error {
		if err := check(); err != nil {
			return err
		}
		now := time.Now()
		guardedSink := sink
		if sink != nil {
			guardedSink = func(ctx context.Context, p night.IntentProposal) error {
				if err := check(); err != nil {
					return err
				}
				return sink(ctx, p)
			}
		}
		guardedRelay := func(ctx context.Context, system, user string) (string, error) {
			if err := check(); err != nil {
				return "", err
			}
			return relay(ctx, system, user)
		}
		opt := night.Options{Relay: guardedRelay, Now: now, TZ: loc, IntentSink: guardedSink, FoldBytes: int64(plan.FoldBytes), BeforeCommit: check}
		res, err := night.Run(ctx, root, day, opt)
		if err != nil {
			return err
		}
		if res.Stopped != "" {
			return errors.New("night rollup deferred")
		}
		digest = res.Digest
		if err := foldNightPeriods(ctx, root, now.In(loc), opt, res.Calls); err != nil {
			return err
		}
		if plan.RetentionDays > 0 || plan.FoldBytes > 0 {
			if err := check(); err != nil {
				return err
			}
			pr, err := prune.Apply(root, prune.Options{RetentionDays: plan.RetentionDays, FoldBytes: int64(plan.FoldBytes), Now: now, TZ: loc})
			if err != nil {
				return err
			}
			digest = nightPruneLine(digest, pr, lang)
		}
		return nil
	})
	return digest, err
}

func foldNightPeriods(ctx context.Context, root string, now time.Time, opt night.Options, spent int) error {
	week := now.AddDate(0, 0, -7)
	month := now.AddDate(0, -1, 0)
	year := now.AddDate(-1, 0, 0)
	folds := []struct {
		day time.Time
		run func(context.Context, string, time.Time, night.Options) (night.Result, error)
	}{
		{week, night.FoldWeek}, {month, night.FoldMonth}, {year, night.FoldYear},
	}
	for _, fold := range folds {
		if spent >= 40 {
			break
		}
		if err := opt.BeforeCommit(); err != nil {
			return err
		}
		res, err := fold.run(ctx, root, fold.day, opt)
		if err != nil {
			return err
		}
		spent += res.Calls
	}
	return nil
}

func nightPruneLine(digest string, pr prune.Report, lang locale.Lang) string {
	for _, fr := range pr.Folded {
		digest += "\n- " + locale.T(lang, "memory.prune-folded", fr.Day, fr.Before, fr.After, fr.Stubs)
	}
	for _, rel := range pr.Expired {
		digest += "\n- " + locale.T(lang, "memory.prune-expired", strings.TrimSuffix(filepath.Base(rel), ".md"))
	}
	for _, kept := range pr.Kept {
		key := "memory.prune-kept-incomplete"
		if kept.Reason == "day summary missing" {
			key = "memory.prune-kept-summary"
		}
		digest += "\n- " + locale.T(lang, key, kept.Day)
	}
	return digest
}

func callNightRelay(ctx context.Context, url string, signer contract.DreamingModelSigner, scope contract.ServiceRequest, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{"model": "", "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
	if err != nil {
		return "", errors.New("night relay unavailable")
	}
	scope.RequestDigest = contract.PolicyDigest(body)
	token, err := signer.IssueDreamingModelService(ctx, scope)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", bytes.NewReader(body)) // #nosec G704 -- server-rendered per-principal relay alias
	if err != nil {
		return "", errors.New("night relay unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(contract.ModelServiceTokenHeader, token)
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req) // #nosec G704 -- server-rendered per-principal relay alias
	if err != nil {
		return "", errors.New("night relay unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("night relay unavailable")
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", errors.New("night relay unavailable")
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(answer, &result) != nil || len(result.Choices) == 0 {
		return "", errors.New("night relay unavailable")
	}
	return result.Choices[0].Message.Content, nil
}
