// Command billing issues refunds. It depends on libs/authz.
//
// Nothing depends on billing, which makes it the leaf used to prove the
// extractor produces no false positives.
package main

import (
	"errors"
	"fmt"

	"github.com/jpadams/jfrog-2026/monorepo/libs/authz"
	"github.com/jpadams/jfrog-2026/monorepo/proto/gen/go/userpb"
)

var errForbidden = errors.New("billing: refund not permitted")

// refund is the unit under test.
func refund(u *userpb.User, cents int) (int, error) {
	if !authz.Can(u, authz.ActionRefund) {
		return 0, errForbidden
	}
	if cents <= 0 {
		return 0, errors.New("billing: refund must be positive")
	}
	return cents, nil
}

func main() {
	admin := &userpb.User{Id: "u-admin", Role: userpb.Role_ROLE_ADMIN}
	amount, err := refund(admin, 250)
	fmt.Println("billing: refund ->", amount, err)
}
