package stack

import (
	"slices"
)

// browserReadTools covers navigation and observation only; none of them can
// submit a form, execute page script or accept a dialog.
var browserReadTools = []string{
	"browser_navigate", "browser_navigate_back", "browser_tabs", "browser_snapshot",
	"browser_take_screenshot", "browser_wait_for", "browser_console_messages",
	"browser_network_requests", "browser_network_request", "browser_resize",
	"browser_close", "browser_pdf_save", "browser_find",
}

// browserMutationTools can change state outside the runtime (form submits,
// uploads, dialogs, arbitrary page script). They are rendered only when the
// owner enables the explicit browser_act capability.
var browserMutationTools = []string{
	"browser_click", "browser_drag", "browser_drop", "browser_hover",
	"browser_type", "browser_fill_form", "browser_press_key", "browser_select_option",
	"browser_file_upload", "browser_handle_dialog", "browser_evaluate", "browser_run_code_unsafe",
	"browser_mouse_click_xy", "browser_mouse_down", "browser_mouse_drag_xy",
	"browser_mouse_move_xy", "browser_mouse_up", "browser_mouse_wheel",
}

// SPEC-0082 R4.3: the playwright-mcp processes live in the per-space browser
// sidecar. The persistent instance attaches the signed-in Chromium over CDP;
// the guest instance launches an isolated Chromium that can never reach the
// owner profile — each answers streamable HTTP on its own port.
func browserServer(user string, guest, act bool) M {
	port := "8931"
	if guest {
		port = "8932"
	}
	include := slices.Clone(browserReadTools)
	if act {
		include = append(include, browserMutationTools...)
	}
	return M{"url": "http://browser-" + user + ":" + port + "/mcp", "timeout": 120, "tools": MCPTools{Include: include}}
}

func env(names ...string) M {
	m := M{}
	for _, n := range names {
		m[n] = "${" + n + "}"
	}
	return m
}
func stdio(command string, args []string, e M) M {
	return M{"command": command, "args": args, "env": e, "timeout": 120}
}
