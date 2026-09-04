package auth

// Role hierarchy for the multi-user/permissions model, loosely modeled after
// WordPress (named roles bundling a capability set) plus a Linux-style
// strict ranking (each role can do everything the ones below it can):
//
//	owner  -- created at signup, exactly one per account. Only role that can
//	          change the account's plan or be assigned by nobody (it's never
//	          granted through the invite endpoint).
//	admin  -- manages the team: invite/remove users, change roles (except
//	          the owner's).
//	editor -- can act on suggestions and operational views (accept/reject/
//	          complete), but can't touch users, roles or the plan.
//	viewer -- read-only everywhere.
//
// This is intentionally a fixed set of roles for v1, not a fully custom
// role/permission builder -- see andrho-api's README for the reasoning.
var roleRank = map[string]int{
	"viewer": 1,
	"editor": 2,
	"admin":  3,
	"owner":  4,
}

// ValidRoles lists every assignable role, in ascending privilege order.
var ValidRoles = []string{"viewer", "editor", "admin", "owner"}

// IsValidRole reports whether role is one of the known roles.
func IsValidRole(role string) bool {
	_, ok := roleRank[role]
	return ok
}

// HasAtLeast reports whether role meets or exceeds min in privilege. An
// unknown role never satisfies any minimum (fails closed).
func HasAtLeast(role, min string) bool {
	r, ok1 := roleRank[role]
	m, ok2 := roleRank[min]
	return ok1 && ok2 && r >= m
}
