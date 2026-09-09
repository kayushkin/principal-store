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
)

// Kinds is every kind a principal can be. Two on purpose: a human is something
// that can log in and be assigned work, a group is a named set of humans. A
// service account or an agent is not modelled in v1 — add it here when it is,
// and the 400 below starts naming it.
var Kinds = []string{KindHuman, KindGroup}

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
