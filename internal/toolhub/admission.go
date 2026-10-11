package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
)

type controllerPlan struct {
	WorkloadID        string          `json:"workload_id"`
	DefinitionID      string          `json:"definition_id"`
	DefinitionVersion string          `json:"definition_version"`
	Image             string          `json:"image"`
	Digest            string          `json:"digest"`
	ToolHiveVersion   string          `json:"toolhive_version"`
	SidecarImages     []string        `json:"sidecar_images"`
	Execution         ExecutionPolicy `json:"execution"`
	CredentialMounts  []Mount         `json:"credential_mounts,omitempty"`
	WorkspacePath     string          `json:"workspace_path,omitempty"`
	Definition        ToolDefinition  `json:"definition,omitempty"`
	// Command marks a bounded-cli plan inside a /cli-exec request: the
	// controller creates the sandbox cell and returns its identity in the
	// receipt rather than attesting a shared executor.
	Command string `json:"command,omitempty"`
}

type controllerRelease struct {
	WorkloadID string `json:"workload_id"`
}

// AdmissionReceipt is the controller's proof that the workload was started
// with the requested immutable limits. A successful HTTP status without this
// receipt is only an admission attempt, not execution evidence.
type AdmissionReceipt struct {
	WorkloadID    string          `json:"workload_id"`
	State         string          `json:"state"`
	Enforced      bool            `json:"enforced"`
	Endpoint      string          `json:"endpoint,omitempty"`
	ImageDigest   string          `json:"image_digest,omitempty"`
	SidecarImages []string        `json:"sidecar_images,omitempty"`
	Execution     ExecutionPolicy `json:"execution"`
	// Isolation lists the enforcement scopes the controller actually
	// verified (for example filesystem, network, pid). CLI admission
	// requires all three; the field stays empty where nothing was proven.
	Isolation []string `json:"isolation,omitempty"`
	// CellID is the sandbox container the controller created for a
	// bounded-cli call; Runtime is the OCI tier it was inspected under.
	// Both are required for the receipt to prove cell isolation.
	CellID  string `json:"cell_id,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

func (r AdmissionReceipt) validate(effective EffectiveBinding) error {
	if r.WorkloadID == "" || r.WorkloadID != effective.WorkloadID || r.State != "running" || !r.Enforced {
		return fmt.Errorf("workload controller returned no running enforcement proof")
	}
	definition := effective.Definition
	if definition.Transport == BoundedCLI {
		if len(r.SidecarImages) != 0 || r.Endpoint != "" {
			return fmt.Errorf("workload controller CLI proof carries container fields")
		}
		// The cell receipt proves a real sibling container: identity, a
		// named OCI runtime tier and — for artifact plans — the pinned image
		// digest the cell was created from.
		if r.CellID == "" || len(r.CellID) > 128 || strings.ContainsAny(r.CellID, "\x00\r\n ") {
			return fmt.Errorf("workload controller CLI proof carries no cell identity")
		}
		switch r.Runtime {
		case "runc", "runsc", "kata":
		default:
			return fmt.Errorf("workload controller CLI proof carries no runtime tier")
		}
		if definition.Source.Image != "" && r.ImageDigest != definition.Source.Digest {
			return fmt.Errorf("workload controller CLI proof does not match the pinned artifact image")
		}
		if definition.Source.Image == "" && r.ImageDigest != "" {
			return fmt.Errorf("workload controller CLI proof carries a stray image digest")
		}
		for _, scope := range []string{"filesystem", "network", "pid"} {
			if !slices.Contains(r.Isolation, scope) {
				return fmt.Errorf("workload controller CLI proof misses %s isolation", scope)
			}
		}
		if !reflect.DeepEqual(r.Execution, effective.Definition.Execution) {
			return fmt.Errorf("workload controller proof does not match immutable execution plan")
		}
		return nil
	}
	if r.ImageDigest != definition.Source.Digest || !reflect.DeepEqual(r.SidecarImages, definition.Workload.SidecarImages) || !reflect.DeepEqual(r.Execution, definition.Execution) {
		return fmt.Errorf("workload controller proof does not match immutable execution plan")
	}
	if r.Endpoint != "" {
		if err := ValidateBackendEndpoint(r.Endpoint); err != nil {
			return fmt.Errorf("workload controller returned unsafe endpoint: %w", err)
		}
	}
	return nil
}

// ControllerAdmissionFromEnv connects the gateway to an external enforcement
// service. The service owns Docker/ToolHive limits; this package only submits
// the immutable plan and fails closed on every non-success response.
func ControllerAdmissionFromEnv() (WorkloadAdmission, error) {
	verifier, err := ControllerAdmissionVerifierFromEnv()
	if err != nil || verifier == nil {
		return nil, err
	}
	return func(ctx context.Context, effective EffectiveBinding) error {
		_, err := verifier(ctx, effective)
		return err
	}, nil
}

// ControllerAdmissionVerifierFromEnv connects the gateway to an external
// controller that returns proof of actual Docker/ToolHive enforcement.
func ControllerAdmissionVerifierFromEnv() (func(context.Context, EffectiveBinding) (AdmissionReceipt, error), error) {
	endpoint := os.Getenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return nil, err
	}
	token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
	// Cold admission may start ToolHive and its isolated proxy container.
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, effective EffectiveBinding) (AdmissionReceipt, error) {
		if effective.Definition.Transport != ContainerMCP {
			return AdmissionReceipt{}, nil
		}
		workspace, err := OpenWorkloadWorkspace(envOr("HUB_STATE", "/state"), effective, injectJobID(ctx, effective))
		if err != nil {
			return AdmissionReceipt{}, err
		}
		return submitPlan(ctx, client, endpoint, token, controllerPlan{
			WorkspacePath:     workspace.Path,
			WorkloadID:        effective.WorkloadID,
			DefinitionID:      effective.Definition.DefinitionID,
			DefinitionVersion: effective.Definition.Version,
			Image:             effective.Definition.Source.Image,
			Digest:            effective.Definition.Source.Digest,
			ToolHiveVersion:   effective.Definition.Workload.ToolHiveVersion,
			SidecarImages:     effective.Definition.Workload.SidecarImages,
			Execution:         effective.Definition.Execution,
			CredentialMounts:  effective.CredentialMounts,
			Definition:        effective.Definition,
		}, effective)
	}, nil
}

// CLIExecFunc is the runner's execution seam: one call submits the exec
// request to the controller and returns the bounded output plus the cell
// receipt. Nil means no controller is wired and every call fails closed.
type CLIExecFunc func(context.Context, cliExecRequest) (cliExecResponse, error)

// CLIExecFromEnv connects the runner to the controller's /cli-exec endpoint;
// the URL derives from the shared admission endpoint, like the releaser.
func CLIExecFromEnv() (CLIExecFunc, error) {
	endpoint := os.Getenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return nil, err
	}
	token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
	execURL := strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/admit") + "/cli-exec"
	// Cells may outlive a short client timeout: the per-request deadline is
	// plan timeout + cell overhead, set on the context by the caller.
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, request cliExecRequest) (cliExecResponse, error) {
		body, err := json.Marshal(request)
		if err != nil {
			return cliExecResponse{}, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, execURL, bytes.NewReader(body)) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return cliExecResponse{}, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(httpReq) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return cliExecResponse{}, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			detail, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))
			return cliExecResponse{}, fmt.Errorf("controller returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
		}
		var execResponse cliExecResponse
		decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&execResponse); err != nil {
			return cliExecResponse{}, fmt.Errorf("invalid controller cli response: %w", err)
		}
		return execResponse, nil
	}, nil
}

// CLIReleaseFromEnv posts /cli-release so a disabled or revoked binding drops
// its warm cells and a finished job drops its task cells; the releaser mirrors
// the /admit -> /release derivation.
func CLIReleaseFromEnv() (func(context.Context, cliReleaseRequest) error, error) {
	endpoint := os.Getenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return nil, err
	}
	token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
	releaseURL := strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/admit") + "/cli-release"
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, release cliReleaseRequest) error {
		body, err := json.Marshal(release)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, releaseURL, bytes.NewReader(body)) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
			return fmt.Errorf("controller cli release returned %s", response.Status)
		}
		return nil
	}, nil
}

// submitPlan posts one immutable controller plan and returns a validated
// receipt. HTTP success without a strict receipt is an attempt, never proof.
func submitPlan(ctx context.Context, client *http.Client, endpoint, token string, plan controllerPlan, effective EffectiveBinding) (AdmissionReceipt, error) {
	body, err := json.Marshal(plan)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body)) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
	if err != nil {
		return AdmissionReceipt{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
	if err != nil {
		return AdmissionReceipt{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))
		return AdmissionReceipt{}, fmt.Errorf("controller returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	if response.StatusCode == http.StatusNoContent {
		return AdmissionReceipt{}, fmt.Errorf("controller returned no enforcement receipt")
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		return AdmissionReceipt{}, err
	}
	if len(responseBody) > 64*1024 {
		return AdmissionReceipt{}, fmt.Errorf("controller receipt too large")
	}
	var receipt AdmissionReceipt
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return AdmissionReceipt{}, fmt.Errorf("invalid controller receipt: %w", err)
	}
	if err := receipt.validate(effective); err != nil {
		return AdmissionReceipt{}, err
	}
	return receipt, nil
}

func ControllerAdmissionReleaserFromEnv() (func(context.Context, string) error, error) {
	endpoint := os.Getenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return nil, err
	}
	token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, workloadID string) error {
		if workloadID == "" {
			return fmt.Errorf("%w: empty workload release", ErrInvalid)
		}
		body, err := json.Marshal(controllerRelease{WorkloadID: workloadID})
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/admit")+"/release", bytes.NewReader(body)) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request) // #nosec G704 -- endpoint was restricted to private ToolHive/controller hosts above.
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
			return fmt.Errorf("controller release returned %s", response.Status)
		}
		return nil
	}, nil
}
