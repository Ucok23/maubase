// Package ownerauth is the owner plane: the small team running this
// deployment, distinct from internal/auth's customer plane (the end users
// of whatever app is built on top). Nothing in here is reachable through
// the OAuth authorization server in internal/oauth — an owner account is
// never something a third-party app can be granted access to.
package ownerauth

import "time"

// Role is a fixed, linearly-ranked set of capabilities — not general RBAC.
// That's deliberate: this is sized for one small team running one
// deployment (see spec/owner-plane.md), not a permissions engine.
type Role string

const (
	// RoleOwner can do everything, including managing other owners. The
	// last remaining owner can't be deleted or demoted (see
	// Service.DeleteOwner) so the team can never lock itself out.
	RoleOwner Role = "owner"
	// RoleAdmin manages schema, OAuth clients, and can see everything, but
	// can't manage other owner-plane accounts.
	RoleAdmin Role = "admin"
	// RoleDeveloper can read/write schema and data for building, but has
	// no access to security-sensitive operations (signing keys, OAuth
	// client secrets, other owner-plane accounts).
	RoleDeveloper Role = "developer"
	// RoleViewer is read-only.
	RoleViewer Role = "viewer"
)

// rank gives roles a total order for "at least this role" checks. Higher
// is more privileged.
var rank = map[Role]int{
	RoleViewer:    1,
	RoleDeveloper: 2,
	RoleAdmin:     3,
	RoleOwner:     4,
}

// IsValid reports whether r is one of the known roles.
func (r Role) IsValid() bool {
	_, ok := rank[r]
	return ok
}

// AtLeast reports whether r meets or exceeds min in the role hierarchy.
// An invalid role never meets any bar.
func (r Role) AtLeast(min Role) bool {
	rr, ok := rank[r]
	if !ok {
		return false
	}
	mr, ok := rank[min]
	if !ok {
		return false
	}
	return rr >= mr
}

type Owner struct {
	ID        string
	Email     string
	Role      Role
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Session struct {
	ID        string
	OwnerID   string
	Token     string // raw token, only ever populated right after creation
	ExpiresAt time.Time
}

// SessionCookieName is deliberately distinct from auth.SessionCookieName:
// an owner-plane session must never be mistaken for (or accepted as) a
// customer-plane one, or vice versa.
const SessionCookieName = "baas_owner_session"

// SessionTTL is shorter than the customer plane's: owner sessions guard
// destructive/administrative operations, so a shorter idle window before
// re-authentication is the right trade-off.
const SessionTTL = 7 * 24 * time.Hour
