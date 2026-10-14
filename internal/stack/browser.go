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

func browserServer(guest, act bool) M {
	args := []string{"/opt/browser/node_modules/@playwright/mcp/cli.js",
		"--caps", "vision,pdf",
		"--output-dir", "/workspace/browser",
		"--output-max-size", "268435456",
		"--block-service-workers"}
	if guest {
		// Isolated launches its own Chromium with an in-memory profile: it
		// cannot reach /state/browser cookies, storage or the CDP session.
		args = append(args, "--isolated", "--executable-path", "/usr/bin/chromium", "--no-sandbox")
	} else {
		args = append(args, "--cdp-endpoint", "http://127.0.0.1:9222")
	}
	include := slices.Clone(browserReadTools)
	if act {
		include = append(include, browserMutationTools...)
	}
	server := stdio("node", args, nil)
	server["tools"] = MCPTools{Include: include}
	return server
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
