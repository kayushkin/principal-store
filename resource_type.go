package principalstore

import (
	"fmt"
	"strconv"
	"strings"
)

// The resource-type vocabulary is served (GET /resource-types) for the same
// reason kinds are: no caller builds a picker out of whatever values happen to
// be in the rows. The five names are exactly the type names in kanban-store's
// entity-type registry, so a card link and a principal's resource list spell a
// type the same way and a ref moves between them untranslated.
//
// Unlike kind, nothing here is normalised — not case, not whitespace. The type
// and the id arrive in a URL path and are stored as the join key, so a PUT to
// /Instance/x that answered with "instance" would be a silent rewrite of what
// the caller sent. A wrong spelling is a 400 naming the right one instead.

const (
	ResourceTypeAgent    = "agent"
	ResourceTypeInstance = "instance"
	ResourceTypeMachine  = "machine"
	ResourceTypeSkill    = "skill"
	ResourceTypeTool     = "tool"
)

// resourceTypeDefinition is everything this store knows about one resource
// type: who assigns its ids, and what shape those ids take.
type resourceTypeDefinition struct {
	name string
	// owner is the store whose database assigns the id — named in the 400 when
	// the id does not exist. agent, instance and machine are reached through
	// llm-bridge-server, but the rows belong to agent-store and harness-store.
	owner string
	// idDescription says which id is wanted, in the words a 400 uses.
	idDescription string
	// numericID is true when the owner's id is an integer primary key, so
	// anything that is not a plain decimal integer cannot be one.
	numericID bool
	// nameIsNotAnID is appended to a malformed-id 400 when the type has a
	// human-readable name a caller is likely to send instead of the id.
	nameIsNotAnID string
}

var resourceTypeDefinitions = []resourceTypeDefinition{
	{
		name:          ResourceTypeAgent,
		owner:         "agent-store",
		idDescription: "agent-store's numeric agents.id, e.g. 12",
		numericID:     true,
		nameIsNotAnID: "the slug is not accepted: agent-store lets a slug be renamed, so a slug is a name, not an id",
	},
	{
		name:          ResourceTypeInstance,
		owner:         "harness-store",
		idDescription: "harness-store's instance id, e.g. inst-cc-local",
	},
	{
		name:          ResourceTypeMachine,
		owner:         "harness-store",
		idDescription: "harness-store's machine id, e.g. m_localhost",
	},
	{
		name:          ResourceTypeSkill,
		owner:         "skill-store",
		idDescription: "skill-store's numeric skills.id, e.g. 42",
		numericID:     true,
		nameIsNotAnID: "the skill name is not accepted: two skills can share one",
	},
	{
		name:          ResourceTypeTool,
		owner:         "tool-store",
		idDescription: "tool-store's numeric tools.id, e.g. 12",
		numericID:     true,
		nameIsNotAnID: "the tool name is not accepted: two tools can share one",
	},
}

// ResourceTypes is every type a principal's resource list can hold, in the
// order GET /resource-types serves them.
var ResourceTypes = func() []string {
	names := make([]string, 0, len(resourceTypeDefinitions))
	for _, definition := range resourceTypeDefinitions {
		names = append(names, definition.name)
	}
	return names
}()

// lookupResourceType resolves a caller's resource_type to its definition, or
// the 400 that names the whole vocabulary.
func lookupResourceType(raw string) (resourceTypeDefinition, error) {
	for _, definition := range resourceTypeDefinitions {
		if raw == definition.name {
			return definition, nil
		}
	}
	return resourceTypeDefinition{}, ErrUnknownResourceType(raw)
}

// ErrUnknownResourceType names the whole vocabulary, because a caller who
// guessed wrong cannot guess right from a bare rejection.
func ErrUnknownResourceType(raw string) error {
	return fmt.Errorf("%w: unknown resource_type %q: use one of %s",
		ErrInvalidResource, raw, strings.Join(ResourceTypes, ", "))
}

// validateResourceID checks an id has the shape its owner hands out. It does
// not ask the owner whether the id exists; that is the ResourceChecker's job.
//
// Nothing is trimmed: " 12" is refused rather than stored as "12", because a
// caller that sent whitespace has a bug upstream that a silent fix would hide.
// A numeric id must be in canonical form — no sign, no leading zeros — because
// "012" and "12" would otherwise be two rows for one agent.
func validateResourceID(definition resourceTypeDefinition, resourceID string) error {
	if resourceID == "" {
		return fmt.Errorf("%w: %s resource_id is required: send %s",
			ErrInvalidResource, definition.name, definition.idDescription)
	}
	if strings.TrimSpace(resourceID) != resourceID {
		return fmt.Errorf("%w: %s resource_id %q has surrounding whitespace, and nothing is trimmed: send %s exactly",
			ErrInvalidResource, definition.name, resourceID, definition.idDescription)
	}
	if !definition.numericID {
		return nil
	}
	parsed, err := strconv.ParseInt(resourceID, 10, 64)
	if err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == resourceID {
		return nil
	}
	message := fmt.Sprintf("%s resource_id %q is not a plain decimal integer (no sign, no leading zeros): send %s",
		definition.name, resourceID, definition.idDescription)
	if definition.nameIsNotAnID != "" {
		message += " — " + definition.nameIsNotAnID
	}
	return fmt.Errorf("%w: %s", ErrInvalidResource, message)
}
