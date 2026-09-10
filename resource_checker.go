package principalstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ResourceChecker asks a resource's owner whether it exists, so a principal's
// resource list never holds an id nobody hands out.
//
// CheckResourceExists returns nil when the owner has the resource, an error
// wrapping ErrResourceNotFound when the owner answers that it does not, and any
// other error when the owner could not be asked or answered something else. The
// two failures are kept apart because they mean opposite things to a caller: a
// wrong id is theirs to fix, an owner that is down is not.
type ResourceChecker interface {
	CheckResourceExists(ctx context.Context, resourceType, resourceID string) error
}

// Where each owner listens when the environment says nothing. The shipped unit
// sets all three explicitly; the defaults exist so a bare run on this host works.
// LLM_BRIDGE_URL is the name llm-bridge-adapter and dash already read, and
// SKILL_STORE_URL and TOOL_STORE_URL are dash's, so one host has one name per
// owner.
const (
	DefaultLLMBridgeServerURL = "http://127.0.0.1:8160"
	DefaultSkillStoreURL      = "http://127.0.0.1:8301"
	DefaultToolStoreURL       = "http://127.0.0.1:8302"

	llmBridgeServerURLVariable = "LLM_BRIDGE_URL"
	skillStoreURLVariable      = "SKILL_STORE_URL"
	toolStoreURLVariable       = "TOOL_STORE_URL"
)

// LLMBridgeServerURL is where agents, harness instances and machines are
// checked, read from LLM_BRIDGE_URL.
func LLMBridgeServerURL() string {
	return environmentOr(llmBridgeServerURLVariable, DefaultLLMBridgeServerURL)
}

// SkillStoreURL is where skills are checked, read from SKILL_STORE_URL.
func SkillStoreURL() string { return environmentOr(skillStoreURLVariable, DefaultSkillStoreURL) }

// ToolStoreURL is where tools are checked, read from TOOL_STORE_URL.
func ToolStoreURL() string { return environmentOr(toolStoreURLVariable, DefaultToolStoreURL) }

func environmentOr(variable, fallback string) string {
	if value := os.Getenv(variable); value != "" {
		return value
	}
	return fallback
}

// ownerCheckTimeout bounds each owner call. A PUT waits on it, and the caller is
// a person clicking in a picker: an owner that has not answered in three
// seconds is reported as a 502 rather than left to hang the request.
const ownerCheckTimeout = 3 * time.Second

// HTTPResourceChecker asks the real owners over HTTP.
type HTTPResourceChecker struct {
	llmBridgeServerURL string
	skillStoreURL      string
	toolStoreURL       string
	client             *http.Client
}

// NewHTTPResourceChecker builds a checker against the given base URLs.
func NewHTTPResourceChecker(llmBridgeServerURL, skillStoreURL, toolStoreURL string) *HTTPResourceChecker {
	return &HTTPResourceChecker{
		llmBridgeServerURL: strings.TrimSuffix(llmBridgeServerURL, "/"),
		skillStoreURL:      strings.TrimSuffix(skillStoreURL, "/"),
		toolStoreURL:       strings.TrimSuffix(toolStoreURL, "/"),
		client:             &http.Client{Timeout: ownerCheckTimeout},
	}
}

// CheckResourceExists implements ResourceChecker.
func (c *HTTPResourceChecker) CheckResourceExists(ctx context.Context, resourceType, resourceID string) error {
	switch resourceType {
	case ResourceTypeAgent:
		return c.checkAgentInList(ctx, resourceID)
	case ResourceTypeInstance:
		return c.checkByGet(ctx, "llm-bridge-server", llmBridgeServerURLVariable,
			c.llmBridgeServerURL+"/instances/"+url.PathEscape(resourceID))
	case ResourceTypeMachine:
		return c.checkByGet(ctx, "llm-bridge-server", llmBridgeServerURLVariable,
			c.llmBridgeServerURL+"/machines/"+url.PathEscape(resourceID))
	case ResourceTypeSkill:
		return c.checkByGet(ctx, "skill-store", skillStoreURLVariable,
			c.skillStoreURL+"/skills/"+url.PathEscape(resourceID))
	case ResourceTypeTool:
		return c.checkByGet(ctx, "tool-store", toolStoreURLVariable,
			c.toolStoreURL+"/tools/"+url.PathEscape(resourceID))
	}
	// Unreachable through the store, which validates the type first. Reaching it
	// means a type was added to resource_type.go without an owner check here.
	return fmt.Errorf("no owner check for resource_type %q: resource_type.go names %s, and each needs a case in CheckResourceExists",
		resourceType, strings.Join(ResourceTypes, ", "))
}

// get performs one GET and reads the whole body, so every answer below can
// quote the owner's status and body text as it came.
func (c *HTTPResourceChecker) get(ctx context.Context, owner, requestURL string) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build GET %s: %w", requestURL, err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("%s did not answer GET %s: %w", owner, requestURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("%s answered GET %s with %s, but the body could not be read: %w",
			owner, requestURL, response.Status, err)
	}
	return response, body, nil
}

// checkByGet is the check for an owner with a get-by-id route: 2xx exists, 404
// does not, anything else is a failure carrying the owner's status and body.
func (c *HTTPResourceChecker) checkByGet(ctx context.Context, owner, urlVariable, requestURL string) error {
	response, body, err := c.get(ctx, owner, requestURL)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(body))
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusNotFound && text == unroutedNotFoundBody:
		// Go's ServeMux answers a path it has no route for with exactly this
		// body. Every owner here answers a missing record with its own text
		// ("instance not found", {"error":"skill not found"}), so this body means
		// the URL points at the wrong service or an owner without the route.
		// Reading it as "does not exist" would refuse every real resource with a
		// 400 telling the caller their id is wrong.
		return fmt.Errorf("%s answered GET %s with %s %q, which is Go's answer for a route that does not exist, not for a missing record: check that %s points at %s",
			owner, requestURL, response.Status, text, urlVariable, owner)
	case response.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s answered GET %s with %s: %s", ErrResourceNotFound, owner, requestURL, response.Status, text)
	default:
		return fmt.Errorf("%s answered GET %s with %s: %s", owner, requestURL, response.Status, text)
	}
}

const unroutedNotFoundBody = "404 page not found"

// checkAgentInList scans GET /agents for a numeric id. agent-store's
// single-agent route is keyed by slug, and the slug is renameable — a name, not
// an id — so the list is the only place the id can be looked up.
func (c *HTTPResourceChecker) checkAgentInList(ctx context.Context, resourceID string) error {
	const owner = "llm-bridge-server"
	requestURL := c.llmBridgeServerURL + "/agents"
	response, body, err := c.get(ctx, owner, requestURL)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s answered GET %s with %s: %s", owner, requestURL, response.Status, strings.TrimSpace(string(body)))
	}
	var agents []struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &agents); err != nil {
		return fmt.Errorf("%s answered GET %s with a body that is not a JSON array of agents with integer ids: %w",
			owner, requestURL, err)
	}
	for index, agent := range agents {
		if agent.ID == nil {
			return fmt.Errorf("%s answered GET %s with an agent at index %d that has no id", owner, requestURL, index)
		}
		if strconv.FormatInt(*agent.ID, 10) == resourceID {
			return nil
		}
	}
	return fmt.Errorf("%w: %s answered GET %s with %d agents and none has id %s",
		ErrResourceNotFound, owner, requestURL, len(agents), resourceID)
}
