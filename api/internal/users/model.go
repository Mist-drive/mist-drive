package users

import (
	"strings"
	"time"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

const loginHistoryMax = 10

type LoginRecord struct {
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent,omitempty"`
	At        time.Time `json:"at"`
}

type TrustedDevice struct {
	ID          string    `json:"id"`
	HashedToken string    `json:"hashedToken"`
	Label       string    `json:"label"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type PublicDevice struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type User struct {
	ID              string          `json:"id"`
	Login           string          `json:"login"`
	BcryptPwd       string          `json:"bcryptPwd"`
	QuotaBytes      int64           `json:"quotaBytes"`
	UsedBytes       int64           `json:"usedBytes"`
	Role            Role            `json:"role"`
	CreatedAt       time.Time       `json:"createdAt"`
	Email           string          `json:"email,omitempty"`
	TokenVersion    int64           `json:"tokenVersion,omitempty"`
	TOTPSecret      string          `json:"totpSecret,omitempty"`
	TOTPEnabled     bool            `json:"totpEnabled"`
	TOTPBackupCodes []string        `json:"totpBackupCodes,omitempty"`
	TrustedDevices  []TrustedDevice `json:"trustedDevices,omitempty"`
	LoginHistory    []LoginRecord   `json:"loginHistory,omitempty"`
}

func (u *User) AppendLoginRecord(ip, ua string) {
	ip = strings.Clone(ip)
	if runes := []rune(ua); len(runes) > 120 {
		ua = string(runes[:120])
	} else {
		ua = strings.Clone(ua)
	}
	r := LoginRecord{IP: ip, UserAgent: ua, At: time.Now()}
	u.LoginHistory = append([]LoginRecord{r}, u.LoginHistory...)
	if len(u.LoginHistory) > loginHistoryMax {
		u.LoginHistory = u.LoginHistory[:loginHistoryMax]
	}
}

func (u *User) PublicDevices() []PublicDevice {
	now := time.Now()
	out := make([]PublicDevice, 0, len(u.TrustedDevices))
	for _, d := range u.TrustedDevices {
		if now.Before(d.ExpiresAt) {
			out = append(out, PublicDevice{ID: d.ID, Label: d.Label, CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt})
		}
	}
	return out
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
	TOTPEnabled   bool   `json:"totpEnabled"`
	Email         string `json:"email,omitempty"`
}

func (u *User) Public() PublicUser {
	return PublicUser{
		ID: u.ID, Login: u.Login, Role: u.Role,
		QuotaBytes: u.QuotaBytes, UsedBytes: u.UsedBytes,
		TOTPEnabled: u.TOTPEnabled, Email: u.Email,
	}
}
