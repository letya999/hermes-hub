package communication

import (
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/skillstore/gitsource"
)

// skillGitRequest is the /v1/skill-git contract (SPEC-0083 R10.*). Every
// action runs through the caller-scoped owner store and the bounded cell —
// installs and updates produce drafts, never direct publishes.
type skillGitRequest struct {
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path,omitempty"`
	Name   string `json:"name,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Env    string `json:"env,omitempty"`
}

func (g *Gateway) handleSkillGit(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request skillGitRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if decodeJSON(r, &request) != nil {
		http.Error(w, "invalid skill-git request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(g.config.SkillsStore) == "" {
		http.Error(w, "skill store unavailable", http.StatusServiceUnavailable)
		return
	}
	st, err := skillstore.Open(g.config.SkillsStore, skillstore.ScopeUser)
	if err != nil {
		http.Error(w, "skill store unavailable", http.StatusServiceUnavailable)
		return
	}
	cell, err := g.skillGitCell(r, caller, request)
	if err != nil {
		http.Error(w, "credential unavailable", http.StatusBadGateway)
		return
	}
	switch request.Action {
	case "install":
		out, err := gitsource.Install(r.Context(), st, gitsource.InstallOptions{
			URL: request.URL, Ref: request.Ref, Path: request.Path, Name: request.Name,
			GitLabHost: g.config.GitLabHost,
			Meta:       skillstore.DraftMeta{Author: "agent", Reason: "git install " + request.URL, SessionID: caller.RuntimeID},
		}, cell)
		if err != nil {
			http.Error(w, "install failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, out)
	case "update":
		out, err := gitsource.Update(r.Context(), st, request.Name, cell,
			skillstore.DraftMeta{Author: "agent", Reason: "git update " + request.Name, SessionID: caller.RuntimeID})
		if err != nil {
			http.Error(w, "update failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, out)
	case "check_updates":
		out, err := gitsource.CheckUpdates(r.Context(), st, cell)
		if err != nil {
			http.Error(w, "check failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"reports": out})
	case "sources":
		// Sources can only fail on a store root that vanished after Open;
		// an empty inventory is the honest answer for that case.
		out, _ := gitsource.Sources(st)
		writeJSON(w, map[string]any{"sources": out})
	case "remove_source":
		// Name the skills losing update tracking before the mirror goes —
		// the warning is part of the contract (R10.7).
		var affected []string
		if infos, _ := gitsource.Sources(st); infos != nil {
			for _, info := range infos {
				if info.ID == request.Name {
					affected = info.Skills
				}
			}
		}
		removed, err := gitsource.RemoveSource(st, request.Name)
		if err != nil {
			http.Error(w, "remove failed", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"removed": removed, "skills": affected})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// skillGitCell builds the caller-scoped GitOps; a grant leases the repo
// token through the broker so it reaches the cell as GIT_TOKEN env only.
func (g *Gateway) skillGitCell(r *http.Request, caller identity.Envelope, request skillGitRequest) (gitsource.ExecCell, error) {
	cell := gitsource.ExecCell{Exec: g.config.CLIExec, Principal: caller.PrincipalID, ContextID: caller.ContextID}
	if request.Grant == "" {
		return cell, nil
	}
	env := request.Env
	if env == "" {
		env = "GIT_TOKEN"
	}
	token, err := g.config.BrokerApprove.ResolveEnv(r.Context(), caller, "skillgit", "skillgit", request.Grant, env)
	if err != nil {
		return cell, err
	}
	cell.Token = token
	return cell, nil
}
