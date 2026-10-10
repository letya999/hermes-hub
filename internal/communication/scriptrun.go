package communication

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Bounded scheduled-script execution. Starlark gives determinism and
// isolation by language rule: no filesystem, network, randomness or ambient
// clock — a script can only compute and call the host bindings declared
// below. CPU is cut by a step budget, wall time by the caller's context.
const (
	scriptMaxSource    = 16 * 1024
	scriptMaxSteps     = 1_000_000
	scriptMaxEmits     = 4
	scriptMaxEmitBytes = 4 * 1024
)

// ScriptSyntaxError reports why src would fail to compile under the runner's
// file options — used to reject dead-on-arrival schedules at create time.
func ScriptSyntaxError(src string) error {
	_, _, err := starlark.SourceProgramOptions(scriptFileOptions(), "script.star", src, func(string) bool { return true })
	return err
}

func scriptFileOptions() *syntax.FileOptions {
	opts := syntax.LegacyFileOptions()
	opts.TopLevelControl = true
	return opts
}

// RunScript evaluates src and returns the texts passed to emit(). `now` is
// the only clock the script sees (time_now()), keeping runs replayable.
func RunScript(ctx context.Context, src string, now time.Time) ([]string, error) {
	if len(src) == 0 || len(src) > scriptMaxSource {
		return nil, errors.New("script source out of bounds")
	}
	emitted := []string{}
	thread := &starlark.Thread{Name: "scheduled-script"}
	thread.SetMaxExecutionSteps(scriptMaxSteps)
	done := make(chan struct{})
	defer close(done)
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				thread.Cancel(ctx.Err().Error())
			case <-done:
			}
		}()
	}
	globals := starlark.StringDict{
		"time_now": starlark.NewBuiltin("time_now", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.String(now.UTC().Format(time.RFC3339)), nil
		}),
		"emit": starlark.NewBuiltin("emit", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			var text string
			if err := starlark.UnpackArgs("emit", args, nil, "text", &text); err != nil {
				return nil, err
			}
			if len(emitted) >= scriptMaxEmits || len(text) > scriptMaxEmitBytes {
				return nil, errors.New("emit budget exceeded")
			}
			emitted = append(emitted, text)
			return starlark.None, nil
		}),
	}
	if _, err := starlark.ExecFileOptions(scriptFileOptions(), thread, "script.star", src, globals); err != nil {
		return nil, fmt.Errorf("script failed: %w", err)
	}
	return emitted, nil
}
