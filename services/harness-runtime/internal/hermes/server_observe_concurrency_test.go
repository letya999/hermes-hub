package hermes

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConcurrentHermesAdmissionsOverlap(t *testing.T) {
	firstEntered := make(chan struct{}, 1)
	secondEntered := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			http.NotFound(w, r)
			return
		}
		var session struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&session) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch session.ID {
		case "first-session":
			firstEntered <- struct{}{}
		case "second-session":
			secondEntered <- struct{}{}
		}
		<-r.Context().Done()
	}))
	defer api.Close()
	host, port, err := net.SplitHostPort(api.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	s := &runtimeHTTP{}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := s.postHermesRun(firstCtx, ExecuteRequest{JobID: "first"}, hermesRunBody{SessionID: "first-session"}, nil)
		firstDone <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first admission did not reach Hermes")
	}
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, err := s.postHermesRun(secondCtx, ExecuteRequest{JobID: "second"}, hermesRunBody{SessionID: "second-session"}, nil)
		secondDone <- err
	}()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second admission waited for the first")
	}
	cancelFirst()
	if <-firstDone == nil {
		t.Fatal("cancelled first admission reported success")
	}
	cancelSecond()
	if <-secondDone == nil {
		t.Fatal("cancelled second admission reported success")
	}
}
