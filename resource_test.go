package principalstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeResourceChecker stands in for the owners. The zero value says every
// resource exists; missing names the "type/id" refs it says do not; failure,
// when set, is returned for every call as an owner that could not be asked.
type fakeResourceChecker struct {
	mu      sync.Mutex
	missing map[string]bool
	failure error
	calls   []string
}

func (f *fakeResourceChecker) CheckResourceExists(_ context.Context, resourceType, resourceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := resourceType + "/" + resourceID
	f.calls = append(f.calls, ref)
	if f.failure != nil {
		return f.failure
	}
	if f.missing[ref] {
		return fmt.Errorf("%w: the fake owner has no %s", ErrResourceNotFound, ref)
	}
	return nil
}

func (f *fakeResourceChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func decodeAssignments(t *testing.T, body []byte) []ResourceAssignment {
	t.Helper()
	var out []ResourceAssignment
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return out
}

func TestResourceTypesRoute(t *testing.T) {
	srv, _ := newTestServer(t)
	status, body := do(t, srv, "GET", "/resource-types", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `["agent","instance","machine","skill","tool"]` {
		t.Fatalf("GET /resource-types = %d: %s", status, body)
	}
}

func TestResourceRoutesRefuseAnUnknownTypeWithTheVocabulary(t *testing.T) {
	checker := &fakeResourceChecker{}
	srv, _ := newTestServerWithChecker(t, checker)
	priya := post(t, srv, "human", "Priya Raman", "")

	for _, request := range []struct{ method, path string }{
		{"PUT", "/principals/" + priya.ID + "/resources/robot/1"},
		{"DELETE", "/principals/" + priya.ID + "/resources/robot/1"},
		{"GET", "/principals/" + priya.ID + "/resources?resource_type=robot"},
		// Case is not normalised: the type is a stored join key sent in a path.
		{"PUT", "/principals/" + priya.ID + "/resources/Instance/inst-cc-local"},
	} {
		status, body := do(t, srv, request.method, request.path, nil)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "agent, instance, machine, skill, tool") {
			t.Fatalf("%s %s = %d, want 400 naming the vocabulary: %s", request.method, request.path, status, body)
		}
	}
	if checker.callCount() != 0 {
		t.Fatalf("an unknown type reached the owner check %d times", checker.callCount())
	}
}

func TestResourceIDsMustHaveTheOwnersShape(t *testing.T) {
	checker := &fakeResourceChecker{}
	srv, _ := newTestServerWithChecker(t, checker)
	priya := post(t, srv, "human", "Priya Raman", "")
	base := "/principals/" + priya.ID + "/resources/"

	for path, wants := range map[string][]string{
		"agent/argraphments":        {"agents.id", "slug is not accepted"},
		"agent/012":                 {"agents.id", "leading zeros"},
		"agent/-3":                  {"agents.id"},
		"agent/%2012":               {"whitespace"},
		"skill/algorithmic-art":     {"skills.id"},
		"tool/1.5":                  {"tools.id"},
		"instance/inst-cc-local%20": {"whitespace", "harness-store's instance id"},
		"machine/%20m_localhost":    {"whitespace", "harness-store's machine id"},
	} {
		status, body := do(t, srv, "PUT", base+path, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400: %s", path, status, body)
		}
		for _, want := range wants {
			if !strings.Contains(string(body), want) {
				t.Fatalf("PUT %s should say %q, got: %s", path, want, body)
			}
		}
	}
	if checker.callCount() != 0 {
		t.Fatalf("a malformed id reached the owner check %d times", checker.callCount())
	}
	// A slug on DELETE finds nothing and gets the same explanation, not a bare 404.
	status, body := do(t, srv, "DELETE", base+"agent/argraphments", nil)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "slug is not accepted") {
		t.Fatalf("DELETE agent slug = %d: %s", status, body)
	}
}

func TestPutResourceTheOwnerDoesNotHaveIsA400AndWritesNothing(t *testing.T) {
	checker := &fakeResourceChecker{missing: map[string]bool{"instance/inst-gone": true, "agent/999": true}}
	srv, _ := newTestServerWithChecker(t, checker)
	priya := post(t, srv, "human", "Priya Raman", "")

	status, body := do(t, srv, "PUT", "/principals/"+priya.ID+"/resources/instance/inst-gone", nil)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "instance inst-gone does not exist in harness-store") {
		t.Fatalf("missing instance = %d: %s", status, body)
	}
	status, body = do(t, srv, "PUT", "/principals/"+priya.ID+"/resources/agent/999", nil)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "agent 999 does not exist in agent-store") {
		t.Fatalf("missing agent = %d: %s", status, body)
	}
	status, body = do(t, srv, "GET", "/principals/"+priya.ID+"/resources", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("a refused PUT left a row: %d %s", status, body)
	}
}

func TestPutResourceWhenTheOwnerFailsIsA502AndWritesNothing(t *testing.T) {
	checker := &fakeResourceChecker{failure: errors.New("llm-bridge-server answered GET /instances/inst-cc-local with 500 Internal Server Error: boom")}
	srv, _ := newTestServerWithChecker(t, checker)
	priya := post(t, srv, "human", "Priya Raman", "")

	status, body := do(t, srv, "PUT", "/principals/"+priya.ID+"/resources/instance/inst-cc-local", nil)
	if status != http.StatusBadGateway || !strings.Contains(string(body), "500 Internal Server Error: boom") {
		t.Fatalf("owner failure = %d, want 502 carrying the owner's answer: %s", status, body)
	}
	status, body = do(t, srv, "GET", "/health", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"resources":0`) {
		t.Fatalf("a 502 left a row: %d %s", status, body)
	}
}

func TestPutResourceIsIdempotentAndKeepsTheOriginalCreatedAt(t *testing.T) {
	srv, s := newTestServer(t)
	data := post(t, srv, "group", "Data Team", "")
	path := "/principals/" + data.ID + "/resources/instance/inst-cc-local"

	status, body := do(t, srv, "PUT", path, nil)
	if status != http.StatusCreated {
		t.Fatalf("first PUT = %d: %s", status, body)
	}
	var first ResourceAssignment
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if first != (ResourceAssignment{ResourceType: "instance", ResourceID: "inst-cc-local", AssignedTo: data.ID, CreatedAt: first.CreatedAt}) || first.CreatedAt == 0 {
		t.Fatalf("first PUT row = %+v", first)
	}
	// Age the row so a second PUT that rewrote created_at could not hide in the
	// same wall-clock second.
	if _, err := s.db.Exec(`UPDATE principal_resources SET created_at = 1000`); err != nil {
		t.Fatalf("age row: %v", err)
	}
	status, body = do(t, srv, "PUT", path, nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"created_at":1000`) {
		t.Fatalf("second PUT = %d, want 200 with the original created_at: %s", status, body)
	}
	status, body = do(t, srv, "GET", "/health", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"resources":1`) {
		t.Fatalf("health = %d: %s", status, body)
	}
}

func TestPutResourceAcceptsADisabledPrincipal(t *testing.T) {
	srv, _ := newTestServer(t)
	marcus := post(t, srv, "human", "Marcus Feld", "")
	do(t, srv, "POST", "/principals/"+marcus.ID+"/disable", nil)
	status, body := do(t, srv, "PUT", "/principals/"+marcus.ID+"/resources/machine/m_localhost", nil)
	if status != http.StatusCreated {
		t.Fatalf("PUT for a disabled principal = %d: %s", status, body)
	}
	status, body = do(t, srv, "GET", "/principals/"+marcus.ID+"/resources", nil)
	if status != http.StatusOK || len(decodeAssignments(t, body)) != 1 {
		t.Fatalf("GET for a disabled principal = %d: %s", status, body)
	}
}

func TestDeleteResourceNeverAsksTheOwner(t *testing.T) {
	checker := &fakeResourceChecker{}
	srv, _ := newTestServerWithChecker(t, checker)
	priya := post(t, srv, "human", "Priya Raman", "")
	path := "/principals/" + priya.ID + "/resources/skill/42"

	if status, body := do(t, srv, "PUT", path, nil); status != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	callsAfterPut := checker.callCount()
	// The owner goes down, or deletes the skill: the row must still come off.
	checker.mu.Lock()
	checker.failure = errors.New("skill-store did not answer")
	checker.mu.Unlock()

	status, body := do(t, srv, "DELETE", path, nil)
	if status != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", status, body)
	}
	status, body = do(t, srv, "DELETE", path, nil)
	if status != http.StatusNotFound || !strings.Contains(string(body), "not assigned") {
		t.Fatalf("second DELETE = %d, want 404: %s", status, body)
	}
	if checker.callCount() != callsAfterPut {
		t.Fatalf("DELETE called the owner check %d times", checker.callCount()-callsAfterPut)
	}
}

func TestResourceRoutes404ForAMissingPrincipal(t *testing.T) {
	checker := &fakeResourceChecker{}
	srv, _ := newTestServerWithChecker(t, checker)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		path := "/principals/principal_999999/resources/instance/inst-cc-local"
		if method == "GET" {
			path = "/principals/principal_999999/resources"
		}
		status, body := do(t, srv, method, path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s for a missing principal = %d, want 404: %s", method, status, body)
		}
	}
	if checker.callCount() != 0 {
		t.Fatalf("a missing principal reached the owner check")
	}
}

func TestAHumanInheritsTheResourcesOfTheirGroups(t *testing.T) {
	srv, _ := newTestServer(t)
	priya := post(t, srv, "human", "Priya Raman", "")
	data := post(t, srv, "group", "Data Team", "")
	security := post(t, srv, "group", "Security", "")
	for _, path := range []string{
		"/principals/" + data.ID + "/members/" + priya.ID,
		"/principals/" + security.ID + "/members/" + priya.ID,
		"/principals/" + data.ID + "/resources/instance/inst-cc-local",
		"/principals/" + security.ID + "/resources/tool/7",
		"/principals/" + priya.ID + "/resources/instance/inst-cc-local",
		"/principals/" + priya.ID + "/resources/agent/12",
	} {
		if status, body := do(t, srv, "PUT", path, nil); status != http.StatusCreated {
			t.Fatalf("PUT %s = %d: %s", path, status, body)
		}
	}

	status, body := do(t, srv, "GET", "/principals/"+priya.ID+"/resources", nil)
	if status != http.StatusOK {
		t.Fatalf("GET = %d: %s", status, body)
	}
	got := decodeAssignments(t, body)
	want := []string{
		"agent/12 " + priya.ID,
		"instance/inst-cc-local " + priya.ID, // direct before inherited
		"instance/inst-cc-local " + data.ID,
		"tool/7 " + security.ID,
	}
	if len(got) != len(want) {
		t.Fatalf("human's list = %s, want %v", body, want)
	}
	for i, assignment := range got {
		if line := assignment.ResourceType + "/" + assignment.ResourceID + " " + assignment.AssignedTo; line != want[i] {
			t.Fatalf("row %d = %q, want %q (whole list %s)", i, line, want[i], body)
		}
	}

	// A group sees only its own rows: no nesting, and nothing flows up from members.
	status, body = do(t, srv, "GET", "/principals/"+data.ID+"/resources", nil)
	if got := decodeAssignments(t, body); status != http.StatusOK || len(got) != 1 || got[0].AssignedTo != data.ID {
		t.Fatalf("group's list = %d: %s", status, body)
	}

	status, body = do(t, srv, "GET", "/principals/"+priya.ID+"/resources?resource_type=instance", nil)
	if got := decodeAssignments(t, body); status != http.StatusOK || len(got) != 2 {
		t.Fatalf("resource_type=instance = %d: %s", status, body)
	}

	// A disabled group stops contributing, unless asked.
	do(t, srv, "POST", "/principals/"+security.ID+"/disable", nil)
	status, body = do(t, srv, "GET", "/principals/"+priya.ID+"/resources", nil)
	if status != http.StatusOK || strings.Contains(string(body), security.ID) || len(decodeAssignments(t, body)) != 3 {
		t.Fatalf("disabled group still contributes = %d: %s", status, body)
	}
	status, body = do(t, srv, "GET", "/principals/"+priya.ID+"/resources?include_disabled=true", nil)
	if status != http.StatusOK || !strings.Contains(string(body), security.ID) || len(decodeAssignments(t, body)) != 4 {
		t.Fatalf("include_disabled hides the disabled group = %d: %s", status, body)
	}
}

func TestAnEmptyResourceListIsAnArray(t *testing.T) {
	srv, _ := newTestServer(t)
	vlad := post(t, srv, "human", "Vlad Kayushkin", "")
	data := post(t, srv, "group", "Data Team", "")
	for _, id := range []string{vlad.ID, data.ID} {
		status, body := do(t, srv, "GET", "/principals/"+id+"/resources", nil)
		if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
			t.Fatalf("empty list for %s = %d %s, want []", id, status, body)
		}
	}
}

func TestRegisterHandlersRefusesANilChecker(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("RegisterHandlers accepted a nil ResourceChecker")
		}
	}()
	RegisterHandlers(http.NewServeMux(), newTestStore(t), nil)
}
