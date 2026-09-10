package principalstore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newOwnerServer stands in for llm-bridge-server, skill-store and tool-store at
// once, answering the way each does live. Any path it does not know gets Go's
// unrouted 404, as a real ServeMux would.
func newOwnerServer(t *testing.T, agentsBody string, agentsStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/agents":
			w.WriteHeader(agentsStatus)
			w.Write([]byte(agentsBody))
		case "/instances/inst-cc-local", "/instances/inst%2Fslash", "/machines/m_localhost", "/skills/7", "/tools/3":
			w.Write([]byte(`{"id":"exists"}`))
		case "/instances/inst-gone":
			http.Error(w, "instance not found", http.StatusNotFound)
		case "/machines/m_gone":
			http.Error(w, "machine not found", http.StatusNotFound)
		case "/skills/8":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"skill not found"}`))
		case "/tools/4":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"tool not found"}`))
		case "/machines/m_boom":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCheckerAgainst(srv *httptest.Server) *HTTPResourceChecker {
	return NewHTTPResourceChecker(srv.URL+"/", srv.URL, srv.URL)
}

func TestHTTPCheckerReadsTheOwnersAnswer(t *testing.T) {
	srv := newOwnerServer(t, `[{"id":12,"slug":"argraphments"},{"id":9,"slug":"bench"}]`, http.StatusOK)
	checker := newCheckerAgainst(srv)
	ctx := context.Background()

	for _, ref := range [][2]string{
		{ResourceTypeAgent, "12"},
		{ResourceTypeInstance, "inst-cc-local"},
		{ResourceTypeInstance, "inst/slash"}, // path-escaped, not split into two segments
		{ResourceTypeMachine, "m_localhost"},
		{ResourceTypeSkill, "7"},
		{ResourceTypeTool, "3"},
	} {
		if err := checker.CheckResourceExists(ctx, ref[0], ref[1]); err != nil {
			t.Fatalf("%s %s should exist: %v", ref[0], ref[1], err)
		}
	}
	for _, ref := range [][2]string{
		{ResourceTypeAgent, "13"},
		{ResourceTypeInstance, "inst-gone"},
		{ResourceTypeMachine, "m_gone"},
		{ResourceTypeSkill, "8"},
		{ResourceTypeTool, "4"},
	} {
		if err := checker.CheckResourceExists(ctx, ref[0], ref[1]); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("%s %s: err = %v, want ErrResourceNotFound", ref[0], ref[1], err)
		}
	}
}

func TestHTTPCheckerReportsAFailingOwnerAsAFailureNotAMissingResource(t *testing.T) {
	ctx := context.Background()

	srv := newOwnerServer(t, `[]`, http.StatusOK)
	checker := newCheckerAgainst(srv)
	err := checker.CheckResourceExists(ctx, ResourceTypeMachine, "m_boom")
	if err == nil || errors.Is(err, ErrResourceNotFound) || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("500: err = %v, want a failure carrying the status and body", err)
	}

	// A base URL pointing at a service without the route answers Go's unrouted
	// 404. That is a misconfiguration, not a missing instance.
	unrouted := NewHTTPResourceChecker(srv.URL+"/wrong-prefix", srv.URL, srv.URL)
	err = unrouted.CheckResourceExists(ctx, ResourceTypeInstance, "inst-cc-local")
	if err == nil || errors.Is(err, ErrResourceNotFound) || !strings.Contains(err.Error(), "LLM_BRIDGE_URL") {
		t.Fatalf("unrouted 404: err = %v, want a failure naming LLM_BRIDGE_URL", err)
	}

	failingAgents := newOwnerServer(t, `database is locked`, http.StatusInternalServerError)
	err = newCheckerAgainst(failingAgents).CheckResourceExists(ctx, ResourceTypeAgent, "12")
	if err == nil || errors.Is(err, ErrResourceNotFound) || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("agents 500: err = %v", err)
	}

	wrongShape := newOwnerServer(t, `{"agents":[{"id":12}]}`, http.StatusOK)
	err = newCheckerAgainst(wrongShape).CheckResourceExists(ctx, ResourceTypeAgent, "12")
	if err == nil || errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("agents not an array: err = %v, want a failure", err)
	}

	stringIDs := newOwnerServer(t, `[{"id":"12","slug":"argraphments"}]`, http.StatusOK)
	err = newCheckerAgainst(stringIDs).CheckResourceExists(ctx, ResourceTypeAgent, "12")
	if err == nil || errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("agents with string ids: err = %v, want a failure", err)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	err = NewHTTPResourceChecker(goneURL, goneURL, goneURL).CheckResourceExists(ctx, ResourceTypeSkill, "7")
	if err == nil || errors.Is(err, ErrResourceNotFound) || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unreachable: err = %v", err)
	}
}

func TestHTTPCheckerTimesOut(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	checker := NewHTTPResourceChecker(slow.URL, slow.URL, slow.URL)
	if checker.client.Timeout != ownerCheckTimeout || ownerCheckTimeout != 3*time.Second {
		t.Fatalf("client timeout = %v, want 3s", checker.client.Timeout)
	}
	checker.client.Timeout = 50 * time.Millisecond
	err := checker.CheckResourceExists(context.Background(), ResourceTypeTool, "3")
	if err == nil || errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("timeout: err = %v, want a failure", err)
	}
}

func TestEveryResourceTypeHasAnOwnerCheck(t *testing.T) {
	srv := newOwnerServer(t, `[]`, http.StatusOK)
	checker := newCheckerAgainst(srv)
	for _, resourceType := range ResourceTypes {
		err := checker.CheckResourceExists(context.Background(), resourceType, "1")
		if err != nil && strings.Contains(err.Error(), "no owner check") {
			t.Fatalf("%s: %v", resourceType, err)
		}
		if _, err := lookupResourceType(resourceType); err != nil {
			t.Fatalf("%s is served but has no definition: %v", resourceType, err)
		}
	}
}
