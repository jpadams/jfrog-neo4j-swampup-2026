package main

import (
	"testing"

	"github.com/jpadams/jfrog-2026/monorepo/libs/authz"
	"github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"
)

func TestHandle(t *testing.T) {
	viewer := &userpb.User{Id: "u-viewer", Role: userpb.Role_ROLE_VIEWER}
	if got := handle(viewer, authz.ActionRead); got != 200 {
		t.Errorf("viewer read = %d, want 200", got)
	}
	if got := handle(viewer, authz.ActionWrite); got != 403 {
		t.Errorf("viewer write = %d, want 403", got)
	}
}
