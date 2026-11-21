package agenttools

import (
	"context"
	"net/http"
	"net/url"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type runEgressTransport struct {
	proxy     string
	transport *http.Transport
}

func newRunEgressTransport(proxy string) http.RoundTripper {
	target, err := url.Parse(proxy)
	if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil ||
		target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Connections through a protected proxy may never outlive one run's check.
	transport.DisableKeepAlives = true
	transport.GetProxyConnectHeader = func(ctx context.Context, selected *url.URL, _ string) (http.Header, error) {
		delegation, _ := ctx.Value(runDelegationKey{}).(api.RunDelegation)
		if selected.String() == proxy && delegation.Egress != "" {
			return http.Header{
				contract.EgressRunTokenHeader: {delegation.Egress},
				api.HeaderRunID:               {delegation.RunID},
			}, nil
		}
		return nil, nil
	}
	return runEgressTransport{proxy: proxy, transport: transport}
}

func (t runEgressTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	selected, err := http.ProxyFromEnvironment(request)
	if err != nil {
		return nil, err
	}
	delegation, _ := request.Context().Value(runDelegationKey{}).(api.RunDelegation)
	if selected == nil || selected.String() != t.proxy || delegation.Egress == "" {
		return http.DefaultTransport.RoundTrip(request)
	}
	if request.URL.Scheme == "http" {
		request = request.Clone(request.Context())
		request.Header.Set(contract.EgressRunTokenHeader, delegation.Egress)
		request.Header.Set(api.HeaderRunID, delegation.RunID)
	}
	return t.transport.RoundTrip(request)
}
