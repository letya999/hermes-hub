package api

import (
	"testing"
	"time"
)

// CHG-0145: an armed wizard nonce collapses every interactive phase into one
// stable /connect/<id>?k= link — the page renders the live step itself, so a
// rotated OAuth state or reminted broker request never strands the URL the
// channel already delivered.
func TestActionURLPrefersWizardLink(t *testing.T) {
	base := Onboarding{Schema: SchemaVersion, OnboardingID: "onb-wiz", WizardNonce: "wiz-nonce", WizardExpires: time.Now().Add(time.Hour)}
	for _, arm := range []func(*Onboarding){
		func(o *Onboarding) { o.ArmGate(GateCredentials, "form-n", time.Time{}) },
		func(o *Onboarding) { o.ArmGate(GateConfirmSetup, "conf-n", time.Time{}) },
		func(o *Onboarding) { o.ArmGate(GateOAuth, "", time.Time{}) },
	} {
		o := base
		o.Stage = StagePending
		arm(&o)
		o.BrokerRequestID = "broker-req"
		o.BrokerAuthorizationURL = "https://broker.example/connect/broker-req"
		o.ProviderAuthorizationURL = "https://accounts.example.com/authorize?state=st"
		got := ActionURL("http://127.0.0.1:8090", o)
		want := "http://127.0.0.1:8090/connect/onb-wiz?k=wiz-nonce"
		if got != want {
			t.Fatalf("gate %s: ActionURL=%q want %q", o.GateKind(), got, want)
		}
	}
}
