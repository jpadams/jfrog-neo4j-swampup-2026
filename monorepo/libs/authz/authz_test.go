package authz

import (
	"testing"

	"github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"
)

func TestCan(t *testing.T) {
	cases := []struct {
		name string
		role userpb.Role
		act  Action
		want bool
	}{
		{"viewer can read", userpb.Role_ROLE_VIEWER, ActionRead, true},
		{"viewer cannot write", userpb.Role_ROLE_VIEWER, ActionWrite, false},
		{"editor can write", userpb.Role_ROLE_EDITOR, ActionWrite, true},
		{"editor cannot refund", userpb.Role_ROLE_EDITOR, ActionRefund, false},
		{"admin can refund", userpb.Role_ROLE_ADMIN, ActionRefund, true},
		{"unspecified cannot read", userpb.Role_ROLE_UNSPECIFIED, ActionRead, false},
		{"unknown action denied", userpb.Role_ROLE_ADMIN, Action("teleport"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Can(&userpb.User{Id: "u1", Role: tc.role}, tc.act)
			if got != tc.want {
				t.Errorf("Can(role=%s, act=%s) = %v, want %v", tc.role, tc.act, got, tc.want)
			}
		})
	}
}
