package stack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	_ "time/tzdata"
)

func slackEventsHostPort(s Settings) int {
	if s.SlackEventsPort > 0 {
		return s.SlackEventsPort
	}
	return 8081
}
func Doctor(s Settings, secrets map[string]string) []string {
	return DoctorScope(s, secrets, nil)
}
func DoctorScope(s Settings, userSecrets, orgSecrets map[string]string) []string {
	secrets := map[string]string{}
	for key, value := range orgSecrets {
		secrets[key] = value
	}
	issues := []string{}
	for key, value := range userSecrets {
		if _, exists := secrets[key]; exists {
			issues = append(issues, "secret key duplicated between organization and user scope: "+key)
			continue
		}
		secrets[key] = value
	}
	if s.Model == "" {
		issues = append(issues, "set model in agent.yaml")
	}
	if s.ModelURL == "" {
		issues = append(issues, "set model_url to your OpenAI-compatible /v1 endpoint")
	}
	if secrets["OPENAI_API_KEY"] == "" {
		issues = append(issues, "set OPENAI_API_KEY (or local provider's documented nonempty value)")
	}
	for _, f := range Features {
		if !s.Has(f.Name) {
			continue
		}
		for _, key := range f.Requires {
			if secrets[key] == "" {
				issues = append(issues, "set "+key+" for "+f.Name)
			}
		}
	}
	if s.imageCredential() == "FAL_KEY" && secrets["FAL_KEY"] == "" {
		issues = append(issues, "set FAL_KEY for image_gen")
	}
	if s.Has("web") {
		issues = append(issues, s.Web.doctorIssues(secrets)...)
	}
	if s.Has("telegram") && secrets["TELEGRAM_ALLOWED_USERS"] != "" {
		for _, id := range strings.Split(secrets["TELEGRAM_ALLOWED_USERS"], ",") {
			if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(strings.TrimSpace(id)) {
				issues = append(issues, "TELEGRAM_ALLOWED_USERS must contain numeric owner IDs, never '*'")
			}
		}
	}
	if s.Has("ssh") && s.SpaceDir != "" {
		if info, err := os.Lstat(filepath.Join(s.SpaceDir, "connections", "ssh", "config.yaml")); err != nil || !info.Mode().IsRegular() {
			issues = append(issues, "create connections/ssh/config.yaml for ssh (see docs/operations.md)")
		}
	}
	for name, server := range s.MCP {
		for _, values := range []map[string]string{server.Headers, server.Env} {
			for _, value := range values {
				for _, match := range regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`).FindAllStringSubmatch(value, -1) {
					if secrets[match[1]] == "" {
						issues = append(issues, "set "+match[1]+" for MCP "+name)
					}
				}
			}
		}
	}
	return issues
}
