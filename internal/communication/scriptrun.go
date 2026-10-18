package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	if len(src) == 0 || len(src) > scriptMaxSource {
		return errors.New("script source out of bounds")
	}
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
	return runScript(ctx, src, now, "", "", nil)
}

// RunScriptProbe evaluates a watcher probe script: input() returns the
// fetched/extracted body, needs() returns hub-prefetched tool results and,
// when watchRoot is set, file builtins read the owner's mounted watch/
// directory (SPEC-0057 R5.2).
func RunScriptProbe(ctx context.Context, src string, now time.Time, input, watchRoot string, needs []string) ([]string, error) {
	return runScript(ctx, src, now, input, watchRoot, needs)
}

func runScript(ctx context.Context, src string, now time.Time, input, watchRoot string, needs []string) ([]string, error) {
	if len(src) == 0 || len(src) > scriptMaxSource {
		return nil, errors.New("script source out of bounds")
	}
	emitted := []string{}
	thread := &starlark.Thread{Name: "scheduled-script"}
	thread.Print = func(*starlark.Thread, string) {} // Untrusted print output never reaches host logs.
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
		"input": starlark.NewBuiltin("input", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.String(input), nil
		}),
		"needs": starlark.NewBuiltin("needs", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			values := make([]starlark.Value, 0, len(needs))
			for _, n := range needs {
				values = append(values, starlark.String(n))
			}
			return starlark.NewList(values), nil
		}),
	}
	if watchRoot != "" {
		globals["read_file"] = starlark.NewBuiltin("read_file", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			path, err := probeWatchPath(watchRoot, args)
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			return starlark.String(data), nil
		})
		globals["list_dir"] = starlark.NewBuiltin("list_dir", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			path, err := probeWatchPath(watchRoot, args)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			names := make([]starlark.Value, 0, len(entries))
			for _, entry := range entries {
				names = append(names, starlark.String(entry.Name()))
			}
			return starlark.NewList(names), nil
		})
		globals["hash_path"] = starlark.NewBuiltin("hash_path", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			path, err := probeWatchPath(watchRoot, args)
			if err != nil {
				return nil, err
			}
			sum, err := hashWatchPath(path)
			if err != nil {
				return nil, err
			}
			return starlark.String(sum), nil
		})
	}
	if _, err := starlark.ExecFileOptions(scriptFileOptions(), thread, "script.star", src, globals); err != nil {
		return nil, fmt.Errorf("script failed: %w", err)
	}
	return emitted, nil
}

// probeWatchPath confines a script-supplied relative path to watchRoot.
func probeWatchPath(root string, args starlark.Tuple) (string, error) {
	var rel string
	if err := starlark.UnpackArgs("watch path", args, nil, "path", &rel); err != nil {
		return "", err
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("watch path escapes root")
	}
	return filepath.Join(root, clean), nil
}

// hashWatchPath produces a stable digest over a watched file or directory
// tree: relative path, mode bits and content of every file, depth-first.
func hashWatchPath(path string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%o\x00", filepath.ToSlash(rel), info.Mode().Perm())
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(h, f)
			_ = f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
