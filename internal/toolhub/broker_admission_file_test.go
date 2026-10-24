package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestBrokerAdmissionFileReachesProbe(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "built", true: "deferred"}[deferred], func(t *testing.T) {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			key := filepath.Join(root, "broker.private")
			if err := os.WriteFile(key, private, 0600); err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(root, "client.json"), "/run/mcp-secrets/client.json"
			if err := os.WriteFile(source, []byte(`{"test":"synthetic"}`), 0600); err != nil {
				t.Fatal(err)
			}
			released, revoked := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch r.URL.Path {
				case "/v1/credentials/credential_1/grants":
					response = brokerv1.Grant{ID: "grant_1", Active: true}
				case "/v1/leases":
					response = brokerv1.Lease{ID: "lease_1", GrantID: "grant_1"}
				case "/v1/runtime/leases/lease_1/materialize":
					response = brokerv1.Materialized{Env: map[string]string{"CLIENT_FILE": target}, Mounts: []brokerv1.Mount{{Source: source, Target: target, ReadOnly: true}}}
				case "/v1/runtime/leases/lease_1/release":
					released = true
					response = struct{}{}
				case "/v1/grants/grant_1/revoke":
					revoked = true
					response = struct{}{}
				default:
					t.Errorf("unexpected broker path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			oldList := api.LocalMCPToolList
			t.Cleanup(func() { api.LocalMCPToolList = oldList })
			probed := false
			api.LocalMCPToolList = func(ctx context.Context, imported api.ImportedArtifact) ([]api.ToolSpec, error) {
				args, cleanup, err := api.PreflightCredentialArgs(ctx, imported)
				if err != nil {
					return nil, err
				}
				defer cleanup()
				mount := "type=bind,source=" + api.DockerBindSource(source) + ",target=" + target + ",readonly"
				if !strings.Contains(strings.Join(args, " "), mount) {
					t.Errorf("Broker file did not reach probe mount")
				}
				probed = true
				return []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}, nil
			}
			cfg := credentialbroker.Config{URL: server.URL, KeyFile: key, KeyID: "toolhub", Issuer: "hermes-toolhub"}
			c := &ControlPlane{Store: NewStore(), Broker: &cfg, BrokerRuntime: &cfg}
			c.AdmitWithCredentials = func(ctx context.Context, d api.ToolDefinition, env map[string]string) (api.ToolDefinition, error) {
				return api.AdmitWithSubmittedCredentials(ctx, root, d, env)
			}
			d := statefulContainerDefinition()
			d.Source.ArchiveDigest = ""
			d.Credentials = []api.CredentialInput{{Name: "CLIENT_FILE", Required: true, Delivery: "file", Target: target}}
			d.CredentialContractID, d.CredentialContractRevision = "client-file", 1
			d.CredentialContractEnv = map[string]string{"CLIENT_FILE": "CLIENT_FILE"}
			onboarding := api.Onboarding{OnboardingID: "onboard-file"}
			rebuilt := false
			if deferred {
				d.Source.Digest = ""
				d.Source.Repository, d.Source.CommitSHA = "https://github.com/example/custom-mcp", strings.Repeat("a", 40)
				onboarding.Recipe = &api.RecipeResolution{Source: api.ArtifactSource{Repository: d.Source.Repository, CommitSHA: d.Source.CommitSHA}, Connection: api.ConnectionRecipe{AuthBeforeToolsList: true, Fields: []api.ConnectionField{{Name: "CLIENT_FILE", Required: true, Delivery: "file", Target: target}}}}
				c.Reviewer = func(ctx context.Context, source api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
					rebuilt = true
					if source != onboarding.Recipe.Source {
						t.Error("resume lost immutable source pin")
					}
					opts := api.ProbeOptionsFrom(ctx)
					if opts.Values["CLIENT_FILE"] != target || len(opts.Mounts) != 1 || !opts.AllowNetwork {
						t.Error("resume lost Broker probe context")
					}
					result := d
					result.Source.Digest = "sha256:" + strings.Repeat("a", 64)
					result.CredentialContractID, result.CredentialContractRevision, result.CredentialContractEnv = "", 0, nil
					result.Tools, err = api.LocalMCPToolList(ctx, api.ImportedArtifact{Definition: result})
					return api.SourceReview{Definition: result}, err
				}
			}
			admitted, err := c.admitWithBrokerCredential(t.Context(), aliceAuth(), onboarding, d, "credential_1")
			if err != nil || !probed || len(admitted.Tools) != 1 {
				t.Errorf("file admission failed: probed=%v error=%v", probed, err)
			}
			if !released || !revoked {
				t.Errorf("temporary authority leaked: released=%v revoked=%v", released, revoked)
			}
			if deferred && !rebuilt {
				t.Error("unbuilt draft never resumed review")
			}
			if admitted.CredentialContractID != d.CredentialContractID {
				t.Error("resume lost approved Broker contract")
			}
		})
	}
}
