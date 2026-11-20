package runtime

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
)

const hermesModelRunHeader = "X-Hub-Hermes-Run-ID"

func (s *runtimeHTTP) modelRunProxy(target *url.URL) http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(request *httputil.ProxyRequest) {
		request.SetURL(target)
		request.Out.Host = target.Host
	},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
			writeRuntimeError(w, http.StatusBadGateway, locale.T(lang, "run.policy-unavailable"))
		}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chat := r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions"
		models := r.Method == http.MethodGet && r.URL.Path == "/v1/models"
		origin, _, _ := net.SplitHostPort(r.RemoteAddr)
		if (!chat && !models) || !net.ParseIP(origin).IsLoopback() || r.URL.IsAbs() ||
			r.URL.RawPath != "" || r.URL.RawQuery != "" || r.Header.Get("Upgrade") != "" || r.Header.Get("Origin") != "" {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(r.Context())
		runID := r.Header.Get(hermesModelRunHeader)
		r.Header.Del(hermesModelRunHeader)
		r.Header.Del(contract.RunTokenHeader)
		r.Header.Del(contract.ModelServiceTokenHeader)
		r.Header.Del(contract.MemoryRunTokenHeader)
		r.Header.Del(contract.EgressRunTokenHeader)
		r.Header.Del(contract.CommunicationRunTokenHeader)
		r.Header.Del(RunDeliveryHeader)
		r.Header.Del(api.HeaderJobID)
		r.Header.Del(api.HeaderRunID)
		if chat {
			var token string
			lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
			if !validHermesRunID(runID) {
				writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
				return
			}
			if _, err := s.jobForRun(r.Context(), runID); err == nil {
				token, err = s.runTokenFor(r.Context(), runID, contract.ModelAudience)
				if err == nil && token != "" {
					r.Header.Set(api.HeaderRunID, runID)
					r.Header.Set(contract.RunTokenHeader, token)
					proxy.ServeHTTP(w, r)
					return
				}
			}
			writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
