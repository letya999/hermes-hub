package stack

// KnowledgeDoctor reports the knowledge_ui edge for `hubctl doctor`. Under
// M32/#520 the OpenKnowledge data plane is retired: the feature stays valid
// but renders no service, no runtime route and no ui_port edge — the honest
// report is unavailability until a governed-API client exists.
func KnowledgeDoctor(s Settings) []string {
	if !s.Has("knowledge_ui") {
		return nil
	}
	return []string{"Knowledge UI: unavailable — OpenKnowledge cannot mount the vault under ToolHub ownership (M32); a governed-API surface is deferred"}
}
