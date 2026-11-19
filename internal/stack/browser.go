package stack

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
