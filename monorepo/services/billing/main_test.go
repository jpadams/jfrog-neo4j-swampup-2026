package main

import (
	"testing"

	"github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"
)

func TestRefund(t *testing.T) {
	admin := &userpb.User{Id: "u-admin", Role: userpb.Role_ROLE_ADMIN}
	editor := &userpb.User{Id: "u-editor", Role: userpb.Role_ROLE_EDITOR}

	if got, err := refund(admin, 250); err != nil || got != 250 {
		t.Errorf("admin refund = (%d, %v), want (250, nil)", got, err)
	}
	if _, err := refund(editor, 250); err == nil {
		t.Error("editor refund succeeded, want forbidden")
	}
	if _, err := refund(admin, 0); err == nil {
		t.Error("zero refund succeeded, want error")
	}
}
