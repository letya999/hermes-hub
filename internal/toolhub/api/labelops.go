package api

import (
	"context"
	"fmt"
	"strings"
)

// DockerLabelQuery lists live objects by ownership selector over the local
// docker CLI — the ControlPlane.LabelQuery production seam.
func DockerLabelQuery(ctx context.Context, selector map[string]string) (map[string][]string, error) {
	return dockerLabelQuery(ctx, LocalCommand, selector)
}

// DockerLabelPurge deletes one object after re-proving its ownership label —
// the ControlPlane.LabelPurge production seam.
func DockerLabelPurge(ctx context.Context, kind, name string, selector map[string]string) error {
	return dockerLabelPurge(ctx, LocalCommand, kind, name, selector)
}

// dockerLabelQuery lists live objects per kind that carry every selector
// label; an empty selector value means "label present". Listing is
// read-only and label-scoped, so foreign objects never appear.
func dockerLabelQuery(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), selector map[string]string) (map[string][]string, error) {
	var filters []string
	for key, value := range selector {
		filters = append(filters, "label="+key+labelSelectorValue(value))
	}
	out := map[string][]string{}
	for _, query := range []struct {
		kind string
		args []string
	}{
		{"container", []string{"ps", "-a", "--format", "{{.Names}}"}},
		{"volume", []string{"volume", "ls", "--format", "{{.Name}}"}},
		{"network", []string{"network", "ls", "--format", "{{.Name}}"}},
		{"image", []string{"images", "--format", "{{.Repository}}:{{.Tag}}"}},
	} {
		args := append(append([]string{}, query.args...), labelFilterArgs(filters)...)
		body, err := run(ctx, "docker", args...)
		if err != nil {
			return nil, fmt.Errorf("docker label query %s: %w", query.kind, err)
		}
		var names []string
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			out[query.kind] = names
		}
	}
	return out, nil
}

// DockerLabelScan lists the live objects matching a label selector together
// with their full label sets — the ControlPlane.LabelScan production seam the
// reconciler diffs against the store to find orphans. Images are deliberately
// absent: recipe images are shared across connections, never orphan candidates.
func DockerLabelScan(ctx context.Context, selector map[string]string) ([]LabeledResource, error) {
	return dockerLabelScan(ctx, LocalCommand, selector)
}

// dockerLabelScan is the run-injected core of DockerLabelScan.
func dockerLabelScan(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), selector map[string]string) ([]LabeledResource, error) {
	found, err := dockerLabelQuery(ctx, run, selector)
	if err != nil {
		return nil, err
	}
	var resources []LabeledResource
	for _, kind := range []string{"container", "volume", "network"} {
		for _, name := range found[kind] {
			labels, err := dockerObjectLabels(ctx, run, kind, name)
			if err != nil {
				return nil, err
			}
			resources = append(resources, LabeledResource{Kind: kind, Name: name, Labels: labels})
		}
	}
	return resources, nil
}

// dockerObjectLabels re-inspects one object's label set; each kind stores
// them under a different JSON field.
func dockerObjectLabels(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), kind, name string) (map[string]string, error) {
	inspect := dockerInspectLabelsArgs(kind, name)
	if inspect == nil {
		return nil, fmt.Errorf("%w: docker object kind %q", ErrInvalid, kind)
	}
	body, err := run(ctx, "docker", inspect...)
	if err != nil {
		return nil, err
	}
	return dockerScopeLabelMap(body), nil
}

func dockerInspectLabelsArgs(kind, name string) []string {
	switch kind {
	case "container":
		return []string{"inspect", "--format", "{{json .Config.Labels}}", name}
	case "volume", "network":
		return []string{kind, "inspect", "--format", "{{json .Labels}}", name}
	}
	return nil
}

// dockerLabelPurge removes one object after re-proving the ownership label
// is on it — the read-modify-delete gap between a footprint query and the
// rm cannot drop an object whose owner changed underneath.
func dockerLabelPurge(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), kind, name string, selector map[string]string) error {
	var remove []string
	switch kind {
	case "container":
		remove = []string{"rm", "-f", name}
	case "volume", "network":
		remove = []string{kind, "rm", name}
	default:
		return fmt.Errorf("%w: label purge kind %q", ErrInvalid, kind)
	}
	labels, err := dockerObjectLabels(ctx, run, kind, name)
	if err != nil {
		return err
	}
	if labels[LabelConnection] == "" || selector[LabelConnection] == "" {
		return fmt.Errorf("%w: docker label purge on unowned %s %q denied", ErrIsolation, kind, name)
	}
	for key, value := range selector {
		if labels[key] != value {
			return fmt.Errorf("%w: docker label purge on foreign %s %q denied", ErrIsolation, kind, name)
		}
	}
	_, err = run(ctx, "docker", remove...)
	return err
}

func labelSelectorValue(value string) string {
	if value == "" {
		return ""
	}
	return "=" + value
}

func labelFilterArgs(filters []string) []string {
	args := make([]string, 0, len(filters)*2)
	for _, filter := range filters {
		args = append(args, "--filter", filter)
	}
	return args
}
