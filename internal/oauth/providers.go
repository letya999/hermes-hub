package oauth

const (
	GoogleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL     = "https://oauth2.googleapis.com/token"
	GoogleDeviceURL    = "https://oauth2.googleapis.com/device/code"

	SlackAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	SlackTokenURL     = "https://slack.com/api/oauth.v2.access"

	AtlassianAuthorizeURL = "https://auth.atlassian.com/authorize"
	AtlassianTokenURL     = "https://auth.atlassian.com/oauth/token"
	AtlassianAudience     = "api.atlassian.com"

	ProtectedResourceWellKnown   = "/.well-known/oauth-protected-resource"
	AuthorizationServerWellKnown = "/.well-known/oauth-authorization-server"
)

func OfficialProviders() map[string]Provider {
	return map[string]Provider{
		"google": {
			Name:          "google",
			AuthorizeURL:  GoogleAuthorizeURL,
			TokenURL:      GoogleTokenURL,
			DeviceCodeURL: GoogleDeviceURL,
		},
		"slack": {
			Name:         "slack",
			AuthorizeURL: SlackAuthorizeURL,
			TokenURL:     SlackTokenURL,
		},
		"atlassian": {
			Name:         "atlassian",
			AuthorizeURL: AtlassianAuthorizeURL,
			TokenURL:     AtlassianTokenURL,
			Audience:     AtlassianAudience,
		},
	}
}
