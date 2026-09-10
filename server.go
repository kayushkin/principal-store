package principalstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// RegisterHandlers mounts the API on mux. Routes are rooted at / — dash adds
// its own /api/principals prefix and supplies the auth this service has none
// of, exactly as it does for prediction-store, quote-store and job-store. That
// is also why main.go binds 127.0.0.1 and not *: with no auth of its own, the
// front door has to be the only door.
//
// checker is how PUT /principals/{id}/resources/... asks a resource's owner
// whether the resource exists. It is required: a nil checker panics here, at
// boot, rather than at the first PUT.
func RegisterHandlers(mux *http.ServeMux, s *Store, checker ResourceChecker) {
	if checker == nil {
		panic("principal-store: RegisterHandlers needs a ResourceChecker; without one a resource assignment cannot be checked against its owner")
	}
	h := &handler{s: s, checker: checker}
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /kinds", h.kinds)
	mux.HandleFunc("GET /resource-types", h.resourceTypes)

	mux.HandleFunc("GET /principals", h.listPrincipals)
	mux.HandleFunc("POST /principals", h.createPrincipal)
	mux.HandleFunc("GET /principals/{id}", h.getPrincipal)
	mux.HandleFunc("PATCH /principals/{id}", h.patchPrincipal)
	mux.HandleFunc("POST /principals/{id}/disable", h.disablePrincipal)
	mux.HandleFunc("POST /principals/{id}/enable", h.enablePrincipal)
	mux.HandleFunc("GET /principals/{id}/members", h.listMembers)
	mux.HandleFunc("PUT /principals/{group}/members/{member}", h.putMember)
	mux.HandleFunc("DELETE /principals/{group}/members/{member}", h.deleteMember)
	mux.HandleFunc("GET /principals/{id}/groups", h.listGroups)

	mux.HandleFunc("GET /principals/{id}/resources", h.listResources)
	mux.HandleFunc("PUT /principals/{id}/resources/{resource_type}/{resource_id}", h.putResource)
	mux.HandleFunc("DELETE /principals/{id}/resources/{resource_type}/{resource_id}", h.deleteResource)
}

type handler struct {
	s       *Store
	checker ResourceChecker
}

// patchableFields is every key PATCH /principals/{id} accepts, matching the
// json tags on Patch. kind and disabled_at are deliberately absent and are
// refused by name, pointing at what does move them.
var patchableFields = map[string]bool{
	"display_name": true,
	"email":        true,
}

// unpatchableFields maps a key PATCH refuses to the explanation it answers with.
var unpatchableFields = map[string]string{
	"kind":        "kind is fixed at creation — a group that became a human would strand its memberships. Create a new principal instead.",
	"disabled_at": "use POST /principals/{id}/disable or POST /principals/{id}/enable, so that removal is always an explicit act.",
	"id":          "id is assigned by this store and never changes; other stores join on it.",
	"seq":         "seq is assigned by this store and never changes; it generates the id.",
}

func sortedPatchableFields() []string {
	out := make([]string, 0, len(patchableFields))
	for field := range patchableFields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	counts, err := h.s.Counts()
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "counts": counts})
}

func (h *handler) kinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Kinds)
}

func (h *handler) resourceTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ResourceTypes)
}

func (h *handler) listResources(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	resources, err := h.s.ListResources(r.PathValue("id"), query.Get("resource_type"), isTrue(query.Get("include_disabled")))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, resources)
}

func (h *handler) putResource(w http.ResponseWriter, r *http.Request) {
	assignment, created, err := h.s.AssignResource(r.Context(), h.checker,
		r.PathValue("id"), r.PathValue("resource_type"), r.PathValue("resource_id"))
	if respondStoreError(w, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, assignment)
}

func (h *handler) deleteResource(w http.ResponseWriter, r *http.Request) {
	err := h.s.UnassignResource(r.PathValue("id"), r.PathValue("resource_type"), r.PathValue("resource_id"))
	if respondStoreError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func filterFrom(r *http.Request) Filter {
	q := r.URL.Query()
	return Filter{
		Kind:            q.Get("kind"),
		Query:           q.Get("q"),
		IncludeDisabled: isTrue(q.Get("include_disabled")),
		Limit:           int(atoi64(q.Get("limit"))),
		Offset:          int(atoi64(q.Get("offset"))),
	}
}

func (h *handler) listPrincipals(w http.ResponseWriter, r *http.Request) {
	principals, err := h.s.List(filterFrom(r))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, principals)
}

func (h *handler) createPrincipal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind        string `json:"kind"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	if !decode(w, r, &body) {
		return
	}
	created, err := h.s.Create(&Principal{Kind: body.Kind, DisplayName: body.DisplayName, Email: body.Email})
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) getPrincipal(w http.ResponseWriter, r *http.Request) {
	p, err := h.s.Get(r.PathValue("id"), isTrue(r.URL.Query().Get("include_disabled")))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) patchPrincipal(w http.ResponseWriter, r *http.Request) {
	// Decoded strictly so that PATCHing "kind" or "disabled_at" is a loud 400
	// naming what does move them, not a silent no-op that leaves the caller
	// believing the row changed.
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	for field, explanation := range unpatchableFields {
		if _, present := raw[field]; present {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%q cannot be patched: %s", field, explanation))
			return
		}
	}
	// Decoding into a map defeats DisallowUnknownFields, so the allowed keys
	// are checked by hand. Without this a PATCH of {"display_nmae":"…"} answers
	// 200 having changed nothing, and the caller believes the edit landed.
	for field := range raw {
		if !patchableFields[field] {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(
				"unknown field %q: PATCH accepts %s", field, strings.Join(sortedPatchableFields(), ", ")))
			return
		}
	}
	body, err := json.Marshal(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	var patch Patch
	if err := json.Unmarshal(body, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	updated, err := h.s.Patch(r.PathValue("id"), patch)
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *handler) disablePrincipal(w http.ResponseWriter, r *http.Request) {
	p, err := h.s.Disable(r.PathValue("id"))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) enablePrincipal(w http.ResponseWriter, r *http.Request) {
	p, err := h.s.Enable(r.PathValue("id"))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.s.ListMembers(r.PathValue("id"), isTrue(r.URL.Query().Get("include_disabled")))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (h *handler) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.s.ListGroups(r.PathValue("id"), isTrue(r.URL.Query().Get("include_disabled")))
	if respondStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (h *handler) putMember(w http.ResponseWriter, r *http.Request) {
	groupID, memberID := r.PathValue("group"), r.PathValue("member")
	created, err := h.s.AddMember(groupID, memberID)
	if respondStoreError(w, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"group_id": groupID, "member_id": memberID, "created": created})
}

func (h *handler) deleteMember(w http.ResponseWriter, r *http.Request) {
	if respondStoreError(w, h.s.RemoveMember(r.PathValue("group"), r.PathValue("member"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decode reads a JSON body, rejecting unknown fields so a misspelled key is a
// 400 rather than a write that silently drops it.
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		fmt.Printf("principal-store: encode response: %v\n", err)
	}
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func respondStoreError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotAMember), errors.Is(err, ErrNotAssigned):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidPrincipal), errors.Is(err, ErrInvalidMembership), errors.Is(err, ErrInvalidResource):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrResourceOwnerUnavailable):
		// The owner, not this store and not the caller, is what failed.
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
	return true
}

func atoi64(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func isTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
