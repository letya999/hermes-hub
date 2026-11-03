package agenttools

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Retired r4 names are denied by design (docs/tool-design.md Appendix C,
// M31.11 S1): they must never re-enter the model-facing catalog. Compat
// shims may keep dispatching them in-runtime, but they are never cataloged,
// advertised, searchable, grantable or selectable.
func TestCatalogDeniesRetiredNames(t *testing.T) {
	retired := []string{
		"system_guide", "diagnostics", "tools_list", "invoke",
		"watch_subscribe", "watch_pause", "watch_resume", "watch_delete",
		"assignment_update", "assignment_apply", "assignment_pause",
		"assignment_resume", "assignment_progress", "assignment_cancel",
		"assignment_complete", "assignment_get", "assignment_status",
		"routine_get", "routine_update", "routine_pause", "routine_resume",
		"routine_snooze", "routine_delete", "routine_history", "routine_preview",
		"skills_list", "skills_search", "skill_view", "skill_manage",
		"image_inspect", "images_inspect", "image_convert", "image_generate",
		"image_edit", "video_generate", "stt", "tts",
		"code_exec", "code_job_list", "code_job_status", "code_job_stop",
		"exposure_open", "exposure_close", "exposure_status",
		"search_files", "find_files",
		"service_list", "service_status", "service_enable", "service_disable",
		"site_deploy", "site_serve",
		"model_use", "session_use",
	}
	present := map[string]bool{}
	descriptions := map[string]string{}
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience == api.AudienceModel {
			present[spec.Name] = true
			descriptions[spec.Name] = spec.Description
		}
	}
	for _, name := range retired {
		if present[name] {
			t.Errorf("retired r4 name %s re-entered the model-facing catalog", name)
		}
		// Descriptions are scanned for underscored names only: bare words
		// (diagnostics, invoke, stt) are honest English and collide.
		if !strings.Contains(name, "_") {
			continue
		}
		for tool, desc := range descriptions {
			if strings.Contains(desc, name) {
				t.Errorf("%s: description references retired name %q — model-facing text must teach r5 vocabulary", tool, name)
			}
		}
	}
	// Slash-shorthand is still the retired name: `watch_list/pause/resume/delete`
	// teaches watch_pause/watch_resume/watch_delete.
	for tool, desc := range descriptions {
		if strings.Contains(desc, "watch_list/pause") || strings.Contains(desc, "pause/resume/delete") {
			t.Errorf("%s: description teaches retired watch verbs via slash shorthand — name watch_edit/watch_remove", tool)
		}
	}
}
