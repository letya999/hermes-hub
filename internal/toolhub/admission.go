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
}

// AdmissionReceipt is the controller's proof that the workload was started
// with the requested immutable limits. A successful HTTP status without this
// receipt is only an admission attempt, not execution evidence.
type AdmissionReceipt struct {
	WorkloadID    string          `json:"workload_id"`
	State         string          `json:"state"`
	Enforced      bool            `json:"enforced"`
	ImageDigest   string          `json:"image_digest"`
	SidecarImages []string        `json:"sidecar_images"`
	Execution     ExecutionPolicy `json:"execution"`
}

func (r AdmissionReceipt) validate(effective EffectiveBinding) error {
	if r.WorkloadID == "" || r.WorkloadID != effective.WorkloadID || r.State != "running" || !r.Enforced {
		return fmt.Errorf("workload controller returned no running enforcement proof")
	}
	definition := effective.Definition
	if r.ImageDigest != definition.Source.Digest || !reflect.DeepEqual(r.SidecarImages, definition.Workload.SidecarImages) || !reflect.DeepEqual(r.Execution, definition.Execution) {
		return fmt.Errorf("workload controller proof does not match immutable execution plan")
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
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context, effective EffectiveBinding) (AdmissionReceipt, error) {
		if effective.Definition.Transport != ContainerMCP {
			return AdmissionReceipt{}, nil
		}
		body, err := json.Marshal(controllerPlan{
			WorkloadID:        effective.WorkloadID,
			DefinitionID:      effective.Definition.DefinitionID,
			DefinitionVersion: effective.Definition.Version,
			Image:             effective.Definition.Source.Image,
			Digest:            effective.Definition.Source.Digest,
			ToolHiveVersion:   effective.Definition.Workload.ToolHiveVersion,
			SidecarImages:     effective.Definition.Workload.SidecarImages,
			Execution:         effective.Definition.Execution,
		})
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
			return AdmissionReceipt{}, fmt.Errorf("controller returned %s", response.Status)
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
	}, nil
}
