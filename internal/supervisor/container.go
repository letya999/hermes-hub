package supervisor

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *Manager) runtimeAddress(container string, port int, managed bool) string {
	if m.cfg.HostSpacesRoot == "" {
		return "http://127.0.0.1:" + strconv.Itoa(port)
	}
	if managed {
		return "http://" + container + "-ctl:" + strconv.Itoa(controlRelayContainerPort)
	}
	return "http://" + container + ":" + strconv.Itoa(m.cfg.RuntimePort)
}

func (m *Manager) hostBindSource(source string) (string, error) {
	if m.cfg.HostSpacesRoot == "" {
		return source, nil
	}
	rel, err := filepath.Rel(m.cfg.SpacesRoot, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("supervisor bind source is outside spaces")
	}
	root := strings.TrimRight(strings.ReplaceAll(m.cfg.HostSpacesRoot, `\`, "/"), "/")
	// Docker Desktop exposes Windows drive sources under this Linux daemon path.
	if len(root) >= 3 && root[1:3] == ":/" && ((root[0] >= 'a' && root[0] <= 'z') || (root[0] >= 'A' && root[0] <= 'Z')) {
		root = "/run/desktop/mnt/host/" + strings.ToLower(root[:1]) + root[2:]
	}
	if !strings.HasPrefix(root, "/") {
		return "", errors.New("host spaces root must be absolute")
	}
	if rel == "." {
		return root, nil
	}
	return root + "/" + filepath.ToSlash(rel), nil
}

func (m *Manager) containerDockerArgs(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-p" && i+1 < len(args) {
			i++ // Container mode uses private Docker DNS, never host-published ports.
			continue
		}
		out = append(out, args[i])
		if args[i] != "--mount" || i+1 >= len(args) {
			continue
		}
		i++
		mount := args[i]
		if strings.HasPrefix(mount, "type=bind,") {
			fields := strings.Split(mount, ",")
			for j, field := range fields {
				if source, ok := strings.CutPrefix(field, "src="); ok {
					host, err := m.hostBindSource(source)
					if err != nil {
						return nil, err
					}
					fields[j] = "src=" + host
				}
			}
			mount = strings.Join(fields, ",")
		}
		out = append(out, mount)
	}
	return out, nil
}
