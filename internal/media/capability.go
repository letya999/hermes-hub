package media

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// ProviderCLIProxy sends image calls to model.base_url. The model id
	// selects the call: CLIProxyImageEndpointModels use /images/generations
	// and /images/edits. CLIProxyChatImageModels use /chat/completions.
	ProviderCLIProxy     = "cliproxy"
	CLIProxyDefaultModel = "gpt-image-2"
	CLIProxyImageSize    = "1024x1024"
	CLIProxyChatAspect   = "1:1"

	DeliveryWorkspace = "workspace"
	DeliveryURL       = "url"

	// ModelFromChat uses model.default when that id is allowed for the provider.
	ModelFromChat = "chat"
)

// CLIProxyImageEndpointModels are accepted on /v1/images/generations and
// /v1/images/edits. The default model stays in this family.
func CLIProxyImageEndpointModels() []string {
	return []string{
		"gpt-image-1.5",
		CLIProxyDefaultModel,
		"grok-imagine-image",
		"grok-imagine-image-quality",
		"grok-imagine-image-2.0",
	}
}

// CLIProxyChatImageModels are Gemini image ids called through
// /v1/chat/completions. The running images endpoint rejects these ids.
// A chat model such as gemini-3.7-flash-high is not in this list.
func CLIProxyChatImageModels() []string {
	return []string{
		"gemini-2.5-flash-image",
		"gemini-3-pro-image",
		"gemini-3-pro-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
	}
}

// CLIProxyModels is the allowlist for ProviderCLIProxy. An id outside this
// list is rejected before a request is sent.
func CLIProxyModels() []string {
	out := append([]string{}, CLIProxyImageEndpointModels()...)
	return append(out, CLIProxyChatImageModels()...)
}

// ImageGen is the image_gen capability. Empty provider, model, and delivery
// pick the defaults: cliproxy, that provider's default model, and workspace.
// ModelFromChat is kept as written and resolved per call.
type ImageGen struct {
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Delivery string `yaml:"delivery,omitempty"`
}

// Normalize applies defaults and rejects an unknown provider, model, or delivery.
func (g ImageGen) Normalize() (ImageGen, error) {
	g.Provider = strings.TrimSpace(g.Provider)
	g.Model = strings.TrimSpace(g.Model)
	g.Delivery = strings.TrimSpace(g.Delivery)
	if g.Provider == "" {
		g.Provider = ProviderCLIProxy
	}
	if g.Provider != ProviderCLIProxy && g.Provider != FalProvider {
		return ImageGen{}, fmt.Errorf("image provider %q is not configured", g.Provider)
	}
	if g.Delivery == "" {
		g.Delivery = DeliveryWorkspace
	}
	if g.Delivery != DeliveryWorkspace && g.Delivery != DeliveryURL {
		return ImageGen{}, fmt.Errorf("image delivery %q is not configured", g.Delivery)
	}
	if g.Model == "" {
		g.Model = defaultImageModel(g.Provider)
	}
	if g.Model != ModelFromChat && !imageModelAllowed(g.Provider, g.Model) {
		return ImageGen{}, fmt.Errorf("image model %q is not available for %s", g.Model, g.Provider)
	}
	return g, nil
}

// Resolve returns the model id sent to the provider.
func (g ImageGen) Resolve(chatModel string) (string, error) {
	norm, err := g.Normalize()
	if err != nil {
		return "", err
	}
	if norm.Model != ModelFromChat {
		return norm.Model, nil
	}
	chatModel = strings.TrimSpace(chatModel)
	if chatModel == "" || strings.Contains(chatModel, "\x00") || !imageModelAllowed(norm.Provider, chatModel) {
		return "", errors.New("chat model is not an image model for this provider")
	}
	return chatModel, nil
}

// Credential is the environment variable for this provider.
func (g ImageGen) Credential() string {
	if g.Provider == FalProvider {
		return "FAL_KEY"
	}
	return "OPENAI_API_KEY"
}

func defaultImageModel(provider string) string {
	if provider == FalProvider {
		return ImageModel
	}
	return CLIProxyDefaultModel
}

func imageModelAllowed(provider, model string) bool {
	if provider == FalProvider {
		return model == ImageModel
	}
	return slicesContains(CLIProxyModels(), model)
}
