// Package tenancy owns the identity and organization layer: users, accounts,
// and the memberships that connect them.
package tenancy

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type User struct {
	ID              string
	Email           string
	Name            string
	AvatarURL       string
	PasswordHash    string    // may be empty for OAuth-only users
	EmailVerifiedAt time.Time // zero if unverified
	CreatedAt       time.Time
}

type Account struct {
	ID        string
	Slug      string
	Name      string
	Plan      string
	CreatedAt time.Time
}

type Membership struct {
	ID        string
	AccountID string
	UserID    string
	Role      Role
	CreatedAt time.Time
}
