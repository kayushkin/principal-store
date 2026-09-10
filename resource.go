package principalstore

import (
	"context"
	"errors"
	"fmt"
)

// ResourceAssignment is one row of a principal's resource list: an agent,
// harness instance, machine, skill or tool the principal works with.
//
// It is a list, not a lock. Nothing in this store or anywhere else refuses an
// action because a row is absent; a card's agent-dispatch picker reads it to
// put an assignee's instances first, and that is all. Permission grants are
// permission-store's job.
//
// There is no display name. The owner renames things, and a copied name goes
// stale without anyone noticing, so the UI resolves the name live by id.
type ResourceAssignment struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	// AssignedTo is the principal whose row this is. In a human's list it is
	// either the human or one of their groups, which is how a reader tells a
	// direct assignment from an inherited one.
	AssignedTo string `json:"assigned_to"`
	CreatedAt  int64  `json:"created_at"`
}

func scanResourceAssignment(scan func(...any) error) (*ResourceAssignment, error) {
	var a ResourceAssignment
	if err := scan(&a.ResourceType, &a.ResourceID, &a.AssignedTo, &a.CreatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// AssignResource adds a resource to a principal's list once its owner confirms
// the resource exists. Idempotent: created reports whether this call wrote the
// row, and the row returned is always the stored one, so a repeat answers with
// the original created_at.
//
// The checks run in the order a caller can act on them: a missing principal
// (ErrNotFound), a malformed type or id (ErrInvalidResource), then the owner.
// The owner is asked before anything is written, so an owner that is down or
// answering nonsense leaves no row behind (ErrResourceOwnerUnavailable) — a row
// for a resource nobody could confirm is exactly what the check exists to keep
// out.
//
// A disabled principal is accepted, and so is a disabled resource: this is a
// list of what someone works with, and a disabled instance may come back.
func (s *Store) AssignResource(ctx context.Context, checker ResourceChecker, principalID, resourceType, resourceID string) (*ResourceAssignment, bool, error) {
	if _, err := s.getRow(principalID); err != nil {
		return nil, false, err
	}
	definition, err := lookupResourceType(resourceType)
	if err != nil {
		return nil, false, err
	}
	if err := validateResourceID(definition, resourceID); err != nil {
		return nil, false, err
	}
	if err := checker.CheckResourceExists(ctx, resourceType, resourceID); err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return nil, false, fmt.Errorf("%w: %s %s does not exist in %s",
				ErrInvalidResource, resourceType, resourceID, definition.owner)
		}
		return nil, false, fmt.Errorf("%w: could not confirm %s %s exists in %s, so nothing was written: %v",
			ErrResourceOwnerUnavailable, resourceType, resourceID, definition.owner, err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO principal_resources (principal_id, resource_type, resource_id, created_at)
		VALUES (?,?,?,?)`, principalID, resourceType, resourceID, now())
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT resource_type, resource_id, principal_id, created_at
		FROM principal_resources
		WHERE principal_id = ? AND resource_type = ? AND resource_id = ?`, principalID, resourceType, resourceID)
	assignment, err := scanResourceAssignment(row.Scan)
	if err != nil {
		return nil, false, err
	}
	return assignment, affected == 1, nil
}

// UnassignResource removes a resource from a principal's own list.
//
// It never asks the owner. A row whose resource was deleted upstream is the row
// someone most needs to remove, and an owner that is down must not pin a row in
// place. For the same reason the id's shape is only checked once the delete has
// found nothing: a row stored under an older, looser rule stays removable, and a
// caller who sent a slug still gets the 400 that says which id is wanted rather
// than a bare 404.
//
// ErrNotAssigned if the row is not there — including when the resource is only
// inherited from a group, which is removed from the group instead.
func (s *Store) UnassignResource(principalID, resourceType, resourceID string) error {
	if _, err := s.getRow(principalID); err != nil {
		return err
	}
	definition, err := lookupResourceType(resourceType)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`
		DELETE FROM principal_resources
		WHERE principal_id = ? AND resource_type = ? AND resource_id = ?`, principalID, resourceType, resourceID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	if err := validateResourceID(definition, resourceID); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s %s is not on %s's own resource list (a resource inherited from a group is removed from the group)",
		ErrNotAssigned, resourceType, resourceID, principalID)
}

// ListResources returns a principal's resource list, ordered by resource_type,
// resource_id, then the principal's own row before inherited ones, then
// assigned_to.
//
// A group's list is its own rows: groups do not nest. A human's list is their
// own rows plus every row of every group they belong to. The same resource
// assigned both ways appears twice, because both are true, and whoever is about
// to remove the direct row needs to see that the group still carries it.
// Disabled groups contribute nothing unless includeDisabled. The principal
// itself answers even when disabled, as Get does.
//
// resourceType filters when non-empty; an unknown one is ErrInvalidResource.
func (s *Store) ListResources(principalID, resourceType string, includeDisabled bool) ([]*ResourceAssignment, error) {
	principal, err := s.getRow(principalID)
	if err != nil {
		return nil, err
	}
	if resourceType != "" {
		if _, err := lookupResourceType(resourceType); err != nil {
			return nil, err
		}
	}

	rows := `
		SELECT resource_type, resource_id, principal_id, created_at
		FROM principal_resources
		WHERE principal_id = ?`
	args := []any{principalID}
	if principal.Kind == KindHuman {
		inherited := `
		SELECT pr.resource_type, pr.resource_id, pr.principal_id, pr.created_at
		FROM group_members gm
		JOIN principals g ON g.id = gm.group_id
		JOIN principal_resources pr ON pr.principal_id = gm.group_id
		WHERE gm.member_id = ?`
		if !includeDisabled {
			inherited += ` AND g.disabled_at = 0`
		}
		rows += ` UNION ALL ` + inherited
		args = append(args, principalID)
	}

	query := `SELECT resource_type, resource_id, principal_id, created_at FROM (` + rows + `)`
	if resourceType != "" {
		query += ` WHERE resource_type = ?`
		args = append(args, resourceType)
	}
	// principal_id <> ? sorts false (0, the principal's own row) before true.
	query += ` ORDER BY resource_type, resource_id, principal_id <> ?, principal_id`
	args = append(args, principalID)

	return s.queryResourceAssignments(query, args...)
}

func (s *Store) queryResourceAssignments(query string, args ...any) ([]*ResourceAssignment, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Never nil: the empty answer is [] on the wire, not null.
	out := []*ResourceAssignment{}
	for rows.Next() {
		assignment, err := scanResourceAssignment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, assignment)
	}
	return out, rows.Err()
}
