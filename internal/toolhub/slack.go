package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var slackID = regexp.MustCompile(`^[CDGTUW][A-Z0-9]{1,63}$`)
var slackTS = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{6}$`)

func SlackScopes(write bool) []string {
	if write {
		return []string{"chat:write"}
	}
	return []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history"}
}

func SlackDefinition(write bool) ToolDefinition {
	d := CalendarDefinition(write)
	d.DefinitionID = "slack-data-read"
	d.Source.URL = "https://slack.com/api"
	d.Execution.Egress = []string{"slack.com"}
	d.Workload.Rationale = "Workspace and user bound personal Slack OAuth data"
	names := []string{"search", "history", "replies"}
	if write {
		d.DefinitionID = "slack-data-write"
		names = []string{"send", "reply", "update"}
	}
	d.Tools = nil
	for _, name := range names {
		args := []CLIArgument{}
		if name == "search" {
			args = append(args, CLIArgument{Name: "query", Type: "string", Required: true})
		} else {
			args = append(args, CLIArgument{Name: "channel", Type: "string", Required: true})
		}
		if name == "reply" || name == "replies" || name == "update" {
			args = append(args, CLIArgument{Name: "ts", Type: "string", Required: true})
		}
		if write {
			args = append(args, CLIArgument{Name: "text", Type: "string", Required: true})
		} else {
			args = append(args, CLIArgument{Name: "cursor", Type: "string"})
		}
		effect := ReadEffect
		if write {
			effect = WriteEffect
		}
		d.Tools = append(d.Tools, ToolSpec{Name: name, Effect: effect, Arguments: args})
	}
	return d
}

type SlackBackend struct{ HTTP *http.Client }

func (b SlackBackend) VerifyAccount(ctx context.Context, token, team, user string) error {
	account, err := b.request(ctx, "auth.test", http.MethodPost, url.Values{}, token)
	if err != nil {
		return err
	}
	if !slackAccountIDs(team, user) || account["team_id"] != team || account["user_id"] != user || account["bot_id"] != nil {
		return ErrUnauthorized
	}
	return nil
}

func (b SlackBackend) Revoke(ctx context.Context, token string) error {
	result, err := b.request(ctx, "auth.revoke", http.MethodPost, url.Values{}, token)
	if err != nil {
		return err
	}
	if result["revoked"] != true {
		return ErrInvalid
	}
	return nil
}

func (b SlackBackend) Call(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, e, t, args, nil)
}
func (b SlackBackend) CallEnv(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any, env map[string]string) (BackendResult, error) {
	if err := RejectAuthorityArguments(args); err != nil {
		return BackendResult{}, err
	}
	write := t.Effect == WriteEffect
	d := SlackDefinition(write)
	if !definitionsEqual(e.Definition, d) || e.Connection == nil || env["ACCESS_TOKEN"] == "" {
		return BackendResult{}, ErrUnauthorized
	}
	known := false
	for _, spec := range d.Tools {
		if spec.Name == t.Name && spec.Effect == t.Effect {
			known = true
		}
	}
	if !known {
		return BackendResult{}, ErrUnauthorized
	}
	for _, scope := range SlackScopes(write) {
		if !slices.Contains(strings.Fields(strings.ReplaceAll(env["OAUTH_SCOPE"], ",", " ")), scope) {
			return BackendResult{}, ErrUnauthorized
		}
	}
	form := url.Values{}
	for key, value := range args {
		valid := false
		for _, a := range t.Arguments {
			if key == a.Name {
				valid = true
			}
		}
		text, ok := value.(string)
		if !valid || !ok || len(text) > 4096 || strings.ContainsRune(text, 0) {
			return BackendResult{}, ErrInvalid
		}
		form.Set(key, text)
	}
	for _, a := range t.Arguments {
		if a.Required && form.Get(a.Name) == "" {
			return BackendResult{}, ErrInvalid
		}
	}
	if t.Name != "search" {
		channel := form.Get("channel")
		if !slackID.MatchString(channel) || !strings.Contains("CDG", channel[:1]) {
			return BackendResult{}, ErrInvalid
		}
	}
	if ts := form.Get("ts"); ts != "" && !slackTS.MatchString(ts) {
		return BackendResult{}, ErrInvalid
	}
	account, err := b.request(ctx, "auth.test", http.MethodPost, url.Values{}, env["ACCESS_TOKEN"])
	if err != nil {
		return BackendResult{}, err
	}
	team, user := e.Connection.Metadata["slack_team"], e.Connection.Metadata["slack_user"]
	if !slackAccountIDs(team, user) || account["team_id"] != team || account["user_id"] != user || account["bot_id"] != nil {
		return BackendResult{}, ErrUnauthorized
	}
	method, verb := "", http.MethodGet
	switch t.Name {
	case "search":
		method = "search.messages"
		form.Set("count", "15")
	case "history":
		method = "conversations.history"
		form.Set("limit", "15")
	case "replies":
		method = "conversations.replies"
		form.Set("limit", "15")
	case "send", "reply":
		method, verb = "chat.postMessage", http.MethodPost
		if t.Name == "reply" {
			form.Set("thread_ts", form.Get("ts"))
			form.Del("ts")
		}
	case "update":
		method, verb = "chat.update", http.MethodPost
	default:
		return BackendResult{}, ErrInvalid
	}
	result, err := b.request(ctx, method, verb, form, env["ACCESS_TOKEN"])
	if err != nil {
		return BackendResult{}, err
	}
	receipt := ""
	if !write {
		messages := result["messages"]
		if t.Name == "search" {
			data, ok := messages.(map[string]any)
			if !ok {
				return BackendResult{}, ErrInvalid
			}
			messages = data["matches"]
		}
		if _, ok := messages.([]any); !ok {
			return BackendResult{}, ErrInvalid
		}
	}
	if write {
		channel, ok := result["channel"].(string)
		ts, valid := result["ts"].(string)
		if !ok || !valid || channel != form.Get("channel") || !slackTS.MatchString(ts) {
			return BackendResult{}, ErrInvalid
		}
		if t.Name == "update" && ts != form.Get("ts") {
			return BackendResult{}, ErrInvalid
		}
		receipt = SafeReceipt(team + ":" + channel + ":" + ts)
		if receipt == "" {
			return BackendResult{}, ErrInvalid
		}
		result["receipt"] = receipt
	}
	return BackendResult{Structured: result, Receipt: receipt}, nil
}

func slackAccountIDs(team, user string) bool {
	return slackID.MatchString(team) && strings.HasPrefix(team, "T") && slackID.MatchString(user) && strings.Contains("UW", user[:1])
}

func (b SlackBackend) request(ctx context.Context, method, verb string, form url.Values, token string) (map[string]any, error) {
	endpoint := "https://slack.com/api/" + method
	body := ""
	if verb == http.MethodGet {
		endpoint += "?" + form.Encode()
	} else {
		body = form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, verb, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := b.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("slack request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, ErrInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack HTTP %d", resp.StatusCode)
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result["ok"] != true {
		return nil, ErrUnauthorized
	}
	return result, nil
}

// PersonalProviderBackend never routes mutable endpoints from connection metadata.
type PersonalProviderBackend struct{}

func (b PersonalProviderBackend) Call(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, e, t, args, nil)
}
func (PersonalProviderBackend) CallEnv(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any, env map[string]string) (BackendResult, error) {
	switch e.Definition.DefinitionID {
	case "google-calendar-read", "google-calendar-write":
		return (CalendarBackend{}).CallEnv(ctx, e, t, args, env)
	case "slack-data-read", "slack-data-write":
		return (SlackBackend{}).CallEnv(ctx, e, t, args, env)
	default:
		return BackendResult{}, ErrUnauthorized
	}
}
