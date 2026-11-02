package catalog

import (
	"slices"
	"strings"
)

// Member requires (SPEC-0092 R2.1): every toolset member declares the RBAC
// operations a caller must hold to invoke it. capRights spells the mapping
// out capability id by capability id — a wrong right is a security bug, so
// nothing here is derived by string tricks. Ids absent from the table fall
// back to tools.select: admission still requires the toolset selected and
// the select right held.
var capRights = map[string]string{
	"archives.create":      "workspace.write",
	"archives.unpack":      "workspace.write",
	"artifacts.delete":     "workspace.write",
	"assignments.apply":    "tools.select",
	"assignments.complete": "tools.select",
	"assignments.create":   "tools.select",
	"assignments.delete":   "tools.select",
	"assignments.get":      "tools.select",
	"assignments.list":     "tools.select",
	"assignments.pause":    "tools.select",
	"assignments.plan":     "tools.select",
	"assignments.progress": "tools.select",
	"assignments.resume":   "tools.select",
	"assignments.update":   "tools.select",
	"context.compact":      "memory.remember",
	"context.read":         "memory.read",
	"documents.convert":    "workspace.write",
	"documents.create":     "workspace.write",
	"documents.edit":       "workspace.write",
	"documents.read":       "workspace.read",
	"exposure.close":       "stack.change",
	"exposure.list":        "stack.inspect",
	"exposure.open":        "stack.change",
	"exposure.status":      "stack.inspect",
	"files.list":           "workspace.read",
	"files.read":           "workspace.read",
	"files.search":         "workspace.read",
	"files.write":          "workspace.write",
	"goals.delete":         "tools.select",
	"goals.pause":          "tools.select",
	"goals.read":           "tools.select",
	"goals.resume":         "tools.select",
	"goals.set":            "tools.select",
	"hh.apply":             "tools.select",
	"hh.resumes":           "tools.select",
	"hh.search":            "tools.select",
	"hh.vacancy":           "tools.select",
	"hosting.list":         "stack.inspect",
	"hosting.publish":      "stack.change",
	"hosting.status":       "stack.inspect",
	"hosting.unpublish":    "stack.change",
	"image_gen.edit":       "workspace.write",
	"image_gen.generate":   "workspace.write",
	"images.convert":       "workspace.write",
	"images.inspect":       "workspace.read",
	"images.read":          "workspace.read",
	"inbox.fetch":          "workspace.read",
	"knowledge.list":       "memory.read",
	"knowledge.read":       "memory.read",
	"knowledge.search":     "memory.search",
	"routines.create":      "tools.select",
	"routines.delete":      "tools.select",
	"routines.get":         "tools.select",
	"routines.list":        "tools.select",
	"routines.update":      "tools.select",
	"services.disable":     "stack.change",
	"services.enable":      "stack.change",
	"services.read":        "stack.inspect",
	"settings.read":        "config.read",
	"settings.write":       "config.edit_own",
	"skills.invoke":        "skills.invoke",
	"skills.read":          "skills.list",
	"skills.search":        "skills.list",
	"skills.view":          "skills.list",
	"skills_manage.manage": "skills.draft",
	"ssh.exec":             "tools.select",
	"ssh.list":             "tools.select",
	"ssh.read":             "tools.select",
	"ssh.shell":            "tools.select",
	"ssh.shell_close":      "tools.select",
	"ssh.shell_read":       "tools.select",
	"ssh.shell_send":       "tools.select",
	"ssh.tunnel":           "tools.select",
	"ssh.tunnel_close":     "tools.select",
	"ssh.tunnel_list":      "tools.select",
	"ssh.write":            "tools.select",
	"terminal.exec":        "tools.select",
	"watches.create":       "stack.change",
	"watches.delete":       "stack.change",
	"watches.list":         "observability.read_own_logs",
	"watches.pause":        "stack.change",
	"watches.resume":       "stack.change",
	"watches.sample":       "observability.read_own_logs",
}

// connectorRights maps the fixed connector pack members onto connectors.*
// operations; the pack's verbs are explicit rather than family-derived.
var connectorRights = map[string]string{
	"connector_allow_writes": "connectors.enable",
	"connector_disable":      "connectors.disable",
	"connector_enable":       "connectors.enable",
	"connector_install":      "connectors.prepare",
	"connector_list":         "connectors.discover",
	"connector_reauth":       "connectors.prepare",
	"connector_remove":       "connectors.remove",
	"connector_search":       "connectors.discover",
	"connector_status":       "connectors.discover",
}

// RequiredRight is the exported form of rightForCap for contract surfaces
// that must declare a tool's RBAC requirement (agent-tools ToolSpec).
func RequiredRight(id string) string { return rightForCap(id) }

// rightForCap resolves one tool-level capability id to its required RBAC
// operation; unknown ids land on the tools.select floor.
func rightForCap(id string) string {
	if op, ok := capRights[id]; ok {
		return op
	}
	return "tools.select"
}

// annotateToolsets attaches actions — the tool-level capability ids the
// family carries (R2.1: capability ids become toolset actions) — and
// requires — the RBAC operations every member demands — to each generated
// toolset.
func annotateToolsets(out map[string]Toolset, caps CapabilitiesDoc) {
	memberCaps := map[string][]string{}
	for id, cap := range caps.Capabilities {
		for _, tool := range cap.Tools {
			memberCaps[tool] = append(memberCaps[tool], id)
		}
	}
	for name, ts := range out {
		family := strings.TrimPrefix(name, "hub:")
		var actions []string
		for id := range caps.Capabilities {
			if strings.SplitN(id, ".", 2)[0] == family {
				actions = append(actions, id)
			}
		}
		requires := map[string][]string{}
		for _, member := range ts.Members {
			rights := map[string]bool{}
			if op, ok := connectorRights[member]; ok {
				rights[op] = true
			}
			for _, id := range memberCaps[member] {
				rights[rightForCap(id)] = true
			}
			if len(rights) == 0 {
				rights["tools.select"] = true
			}
			list := make([]string, 0, len(rights))
			for op := range rights {
				list = append(list, op)
			}
			slices.Sort(list)
			requires[member] = list
		}
		ts.Actions = sorted(actions)
		ts.Requires = requires
		out[name] = ts
	}
}
