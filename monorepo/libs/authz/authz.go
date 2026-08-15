// Package authz decides what a user is allowed to do.
//
// It is the shared Go library: both services/api and services/billing depend
// on it, so a change here must fan out to both.
package authz

import "github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"

// Action is an operation a caller wants to perform.
type Action string

const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionRefund Action = "refund"
)

// Can reports whether the user may perform the action.
func Can(u *userpb.User, a Action) bool {
	switch a {
	case ActionRead:
		return u.Role != userpb.Role_ROLE_UNSPECIFIED
	case ActionWrite:
		return u.Role == userpb.Role_ROLE_EDITOR || u.Role == userpb.Role_ROLE_ADMIN
	case ActionRefund:
		return u.Role == userpb.Role_ROLE_ADMIN
	default:
		return false
	}
}
