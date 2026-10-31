package api

import "testing"

func TestConfirmationActionDigestBindsReviewedAction(t *testing.T) {
	record := Onboarding{OnboardingID: "onboard-1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", DefinitionID: "connector", DefinitionVersion: "1.0.0", ReviewDigest: "review-1", ConnectionID: "conn-1", CredentialRefID: "ref-1", Permissions: []string{"search"}, Effects: []string{"read"}}
	before := ConfirmationActionDigest(record)
	if !DigestPattern.MatchString(before) {
		t.Errorf("missing action digest: %q", before)
	}
	for name, mutate := range map[string]func(*Onboarding){
		"onboarding":               func(o *Onboarding) { o.OnboardingID = "onboard-2" },
		"owner":                    func(o *Onboarding) { o.PrincipalID = "bob" },
		"context":                  func(o *Onboarding) { o.ContextID = "other" },
		"runtime":                  func(o *Onboarding) { o.RuntimeID = "other" },
		"policy":                   func(o *Onboarding) { o.PolicyVersion = "policy-2" },
		"definition":               func(o *Onboarding) { o.DefinitionVersion = "2.0.0" },
		"definition id":            func(o *Onboarding) { o.DefinitionID = "another" },
		"review":                   func(o *Onboarding) { o.ReviewDigest = "review-2" },
		"connection":               func(o *Onboarding) { o.ConnectionID = "conn-2" },
		"credential":               func(o *Onboarding) { o.CredentialRefID = "ref-2" },
		"broker credential":        func(o *Onboarding) { o.BrokerCredentialID = "cred-2" },
		"broker contract":          func(o *Onboarding) { o.BrokerContractID = "contract-2" },
		"broker contract revision": func(o *Onboarding) { o.BrokerContractRevision++ },
		"recipe":                   func(o *Onboarding) { o.RecipeDigest = "recipe-2" },
		"permission":               func(o *Onboarding) { o.Permissions = []string{"send"} },
		"effect":                   func(o *Onboarding) { o.Effects = []string{"write"} },
		"writes":                   func(o *Onboarding) { o.PendingWrites = true },
		"enabled writes":           func(o *Onboarding) { o.WritesEnabled = true },
	} {
		changed := record
		mutate(&changed)
		if got := ConfirmationActionDigest(changed); got == before {
			t.Errorf("%s not bound", name)
		}
	}
	record.Error = "incidental observation"
	record.Revision++
	record.ConfirmationNonce = "opaque-link-token"
	if got := ConfirmationActionDigest(record); got != before {
		t.Error("incidental metadata changed action digest")
	}
}
