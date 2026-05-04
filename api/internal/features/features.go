package features

// Features lists capabilities that vary between CE and Pro builds.
// The active set is selected at compile time via the `pro` build tag.
type Features struct {
	SSO      bool `json:"sso"`
	AuditLog bool `json:"auditLog"`
}

// Current returns the feature set compiled into this binary.
func Current() Features { return current }
