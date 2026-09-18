package principalstore

import (
	"fmt"
	"strings"
)

// The kind vocabulary is served (GET /kinds) rather than inferred from the
// rows, so no caller ever builds a filter out of whatever values happen to
// exist. Same move as job-store's role families and prediction-store's
// categories.

const (
	KindHuman = "human"
	KindGroup = "group"
	// KindContact is someone outside this deployment who a ticket is *from*:
	// the requester who sent the mail. A contact never logs in, never holds a
	// grant, never joins a group and never administers anything — it exists so
	// a requester has an id, and twenty tickets from one person join on one
	// row instead of on a string that changes when they mail from their phone.
	KindContact = "contact"
)

// Kinds is every kind a principal can be: a human is something that can log in
// and be assigned work, a group is a named set of humans, and a contact is an
// outside person work arrives *from*. A service account or an agent is not
// modelled yet — add it here when it is, and the 400 below starts naming it.
var Kinds = []string{KindHuman, KindGroup, KindContact}

// NormalizeKind resolves a caller's word to a canonical kind.
func NormalizeKind(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	for _, k := range Kinds {
		if key == k {
			return k, true
		}
	}
	return "", false
}

// ErrUnknownKind names the whole vocabulary, because a caller who guessed
// wrong cannot guess right from a bare rejection.
func ErrUnknownKind(raw string) error {
	return fmt.Errorf("%w: unknown kind %q: use one of %s",
		ErrInvalidPrincipal, raw, strings.Join(Kinds, ", "))
}

// ActsInThisDeployment says whether a principal of this kind can be given
// access to anything. A contact cannot: it is the outside party on a ticket.
// grant-store asks this before writing a grant, and the login path asks it
// before issuing a session.
func ActsInThisDeployment(kind string) bool {
	return kind == KindHuman || kind == KindGroup
}
