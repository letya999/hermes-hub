package agenttools

import "errors"

// Settings are published by ToolHub through the authenticated supervisor.
// A direct runtime invocation has no settings authority.
func (t *Tools) SettingsGet(Input) (map[string]any, error) { return nil, errors.ErrUnsupported }
func (t *Tools) SettingsSet(Input) (map[string]any, error) { return nil, errors.ErrUnsupported }
