package agenttools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// tool-design §4.2/§4.3 (M31.2 S1): every model-facing argument carries a
// description, closed vocabularies publish their enum, and structured
// parameters are typed object/array — never opaque JSON strings.
func TestCharterSchemaContract(t *testing.T) {
	def := HubToolsDefinition()
	// Parameters that carry structured JSON: the schema must type them
	// object/array, not string-of-JSON.
	structured := map[string]string{
		"routine_create.args": "object", "routine_create.steps": "array", "routine_create.needs": "array",
		"routine_create.extract": "array",
		"watch_create.args":      "object", "assignment_plan.plan": "object", "assignment_create.plan": "object",
		"assignment_edit.plan": "object", "routine_edit.patch": "object", "tool_call.arguments": "object",
	}
	// Closed vocabularies the charter freezes: these args must publish enum.
	enums := map[string]bool{
		"file_list.root": true, "file_read.root": true, "file_search.root": true, "file_write.root": true,
		"file_edit.root": true, "file_mkdir.root": true, "file_remove.root": true, "file_archive.action": true,
		"media_generate.kind": true,
		"ssh_shell.action":    true, "ssh_tunnel.action": true, "terminal_job.action": true,
		"routine_edit.action": true, "routine_create.kind": true, "routine_create.probe_kind": true,
		"routine_create.deliver_mode": true, "routine_create.notify_mode": true,
		"watch_edit.action": true, "watch_create.sink": true, "watch_create.delivery": true,
		"assignment_edit.action": true, "goal_edit.action": true, "context_edit.action": true,
		"session_edit.action": true, "skill_edit.action": true, "skill_search.source": true,
		"connector_edit.action": true, "connector_search.kind": true,
		"site_publish.mode": true, "site_publish.access": true,
		"browser_open.profile": true, "browser_act.action": true, "browser_capture.kind": true,
		"platform_bootstrap.action": true, "platform_usage.period": true,
	}
	for _, spec := range def.Tools {
		if spec.Audience != api.AudienceModel {
			continue
		}
		seen := map[string]bool{}
		for _, arg := range spec.Arguments {
			if seen[arg.Name] {
				t.Errorf("%s: duplicate argument %q", spec.Name, arg.Name)
			}
			seen[arg.Name] = true
			key := spec.Name + "." + arg.Name
			if arg.Description == "" {
				t.Errorf("%s: argument %q carries no description", spec.Name, arg.Name)
			}
			if want, isStructured := structured[key]; isStructured && arg.Type != want {
				t.Errorf("%s: argument %q must type %s, got %s (opaque JSON strings are not a schema)", spec.Name, arg.Name, want, arg.Type)
			}
			if enums[key] && len(arg.Enum) == 0 {
				t.Errorf("%s: argument %q is a closed vocabulary and must publish enum", spec.Name, arg.Name)
			}
			if len(arg.Enum) > 0 && arg.Type != "string" {
				t.Errorf("%s: enum on non-string argument %q", spec.Name, arg.Name)
			}
			if arg.Type == "array" && arg.Items == "" {
				t.Errorf("%s: array argument %q declares no item type", spec.Name, arg.Name)
			}
		}
	}
}

// The connector facade consumes `target` (normalized from `name`/`id`) and
// `version` — the spec must declare the vocabulary the executor reads.
func TestConnectorInstallDeclaresFacadeArgs(t *testing.T) {
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name != "connector_install" {
			continue
		}
		names := map[string]bool{}
		for _, arg := range spec.Arguments {
			names[arg.Name] = true
		}
		if !names["name"] || !names["version"] || !names["id"] || len(spec.Arguments) != 3 {
			t.Fatalf("connector_install must declare name+id+version, got %v", names)
		}
	}
}

// TestConnectorToolsNeverTakeCredentials (M31.7/#444): connector tools route
// every secret through the protected credential form — a connector argument
// shaped like a credential is a design violation, not a convenience.
func TestConnectorToolsNeverTakeCredentials(t *testing.T) {
	def := HubToolsDefinition()
	banned := regexp.MustCompile(`(?i)secret|password|token|api_?key|credential|bearer|private_?key`)
	for _, tool := range def.Tools {
		if !strings.HasPrefix(tool.Name, "connector_") {
			continue
		}
		for _, arg := range tool.Arguments {
			if banned.MatchString(arg.Name) {
				t.Fatalf("%s: credential-shaped argument %q — secrets travel only via the protected form, never tool arguments", tool.Name, arg.Name)
			}
		}
	}
}
