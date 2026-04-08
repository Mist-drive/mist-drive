package users

import "time"

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type User struct {
	ID         string    `json:"id"`
	Login      string    `json:"login"`
	BcryptPwd  string    `json:"bcryptPwd"`
	QuotaBytes int64     `json:"quotaBytes"`
	UsedBytes  int64     `json:"usedBytes"`
	Role       Role      `json:"role"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Bucket returns the MinIO bucket name for this user.
// The user's UUID already satisfies S3 naming rules (lowercase hex + hyphens,
// 36 chars), so we use it directly — no stored `bucket` field to drift.
func (u *User) Bucket() string { return u.ID }

type PublicUser struct {
	ID            string `json:"id"`
	Login         string `json:"login"`
	Role          Role   `json:"role"`
	QuotaBytes    int64  `json:"quotaBytes"`
	UsedBytes     int64  `json:"usedBytes"`
	ReservedBytes int64  `json:"reservedBytes,omitempty"`
	DiskFreeBytes int64  `json:"diskFreeBytes,omitempty"`
}

func (u *User) Public() PublicUser {
	return PublicUser{
		ID: u.ID, Login: u.Login, Role: u.Role,
		QuotaBytes: u.QuotaBytes, UsedBytes: u.UsedBytes,
	}
}
