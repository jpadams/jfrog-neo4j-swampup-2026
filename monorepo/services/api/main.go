// Command api is the user-facing service. It depends on libs/authz.
package main

import (
	"fmt"

	"github.com/jpadams/jfrog-2026/monorepo/libs/authz"
	"github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"
)

// handle is the unit under test: it resolves a request to a status code.
func handle(u *userpb.User, act authz.Action) int {
	if !authz.Can(u, act) {
		return 403
	}
	return 200
}

func main() {
	admin := &userpb.User{Id: "u-admin", Email: "admin@example.com", Role: userpb.Role_ROLE_ADMIN}
	fmt.Println("api: write ->", handle(admin, authz.ActionWrite))
}
