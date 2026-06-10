package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// totpSetup runs GET /api/totp/setup and returns the secret + URI.
func totpSetup(t *testing.T, f *unitFixture) (secret, uri string) {
	t.Helper()
	resp := doUnit(t, f.app, "GET", "/api/totp/setup", nil, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("totp/setup: want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Secret, out.URI
}

// totpEnable runs the full setup→enable flow and returns the secret and backup codes.
func totpEnable(t *testing.T, f *unitFixture) (secret string, backupCodes []string) {
	t.Helper()
	secret, _ = totpSetup(t, f)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp := doUnit(t, f.app, "POST", "/api/totp/enable", map[string]any{
		"secret": secret, "code": code, "password": "pw",
	}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("totp/enable: want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		BackupCodes []string `json:"backupCodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return secret, out.BackupCodes
}

// ---- Setup ----

func TestTOTPSetup_ReturnsSecretAndURI(t *testing.T) {
	f := newUnitFixture(t)
	secret, uri := totpSetup(t, f)
	if secret == "" {
		t.Fatal("secret is empty")
	}
	if uri == "" {
		t.Fatal("uri is empty")
	}
}

func TestTOTPSetup_RequiresAuth(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "GET", "/api/totp/setup", nil, "")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

// ---- Enable ----

func TestTOTPEnable_Success(t *testing.T) {
	f := newUnitFixture(t)
	_, backupCodes := totpEnable(t, f)
	if len(backupCodes) == 0 {
		t.Fatal("expected backup codes, got none")
	}
}

func TestTOTPEnable_InvalidCode(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpSetup(t, f)
	resp := doUnit(t, f.app, "POST", "/api/totp/enable", map[string]any{
		"secret": secret, "code": "000000", "password": "pw",
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestTOTPEnable_WrongPassword(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpSetup(t, f)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp := doUnit(t, f.app, "POST", "/api/totp/enable", map[string]any{
		"secret": secret, "code": code, "password": "wrong",
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestTOTPEnable_MissingSecret(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/api/totp/enable", map[string]any{
		"code": "123456",
	}, f.userToken)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ---- Login with TOTP ----

func TestLogin_TOTPRequired(t *testing.T) {
	f := newUnitFixture(t)
	totpEnable(t, f)

	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
	}, "")
	if resp.StatusCode != 202 {
		t.Fatalf("want 202 totp_required, got %d", resp.StatusCode)
	}
	var out struct {
		TotpRequired bool `json:"totp_required"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.TotpRequired {
		t.Fatal("expected totp_required=true")
	}
}

func TestLogin_TOTPSuccess(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpEnable(t, f)

	code, _ := totp.GenerateCode(secret, time.Now())
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "totpCode": code,
	}, "")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatal("token is empty")
	}
}

func TestLogin_TOTPWrongCode(t *testing.T) {
	f := newUnitFixture(t)
	totpEnable(t, f)

	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "totpCode": "000000",
	}, "")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestLogin_TOTPBackupCode(t *testing.T) {
	f := newUnitFixture(t)
	_, backupCodes := totpEnable(t, f)

	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "totpCode": backupCodes[0],
	}, "")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestLogin_TOTPBackupCodeOneTimeUse(t *testing.T) {
	f := newUnitFixture(t)
	_, backupCodes := totpEnable(t, f)

	// First use: succeeds.
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "totpCode": backupCodes[0],
	}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("first use: want 200, got %d", resp.StatusCode)
	}

	// Second use of the same code: must fail.
	resp = doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "totpCode": backupCodes[0],
	}, "")
	if resp.StatusCode != 401 {
		t.Fatalf("second use: want 401, got %d", resp.StatusCode)
	}
}

// ---- Disable ----

func TestTOTPDisable_Success(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpEnable(t, f)

	code, _ := totp.GenerateCode(secret, time.Now())
	resp := doUnit(t, f.app, "DELETE", "/api/totp/disable", map[string]any{
		"password": "pw", "code": code,
	}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	// Login should no longer require TOTP.
	resp = doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
	}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("post-disable login: want 200, got %d", resp.StatusCode)
	}
}

func TestTOTPDisable_WrongPassword(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpEnable(t, f)

	code, _ := totp.GenerateCode(secret, time.Now())
	resp := doUnit(t, f.app, "DELETE", "/api/totp/disable", map[string]any{
		"password": "wrongpw", "code": code,
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestTOTPDisable_WrongCode(t *testing.T) {
	f := newUnitFixture(t)
	totpEnable(t, f)

	resp := doUnit(t, f.app, "DELETE", "/api/totp/disable", map[string]any{
		"password": "pw", "code": "000000",
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestTOTPDisable_WithBackupCode(t *testing.T) {
	f := newUnitFixture(t)
	_, backupCodes := totpEnable(t, f)

	resp := doUnit(t, f.app, "DELETE", "/api/totp/disable", map[string]any{
		"password": "pw", "code": backupCodes[0],
	}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
}

// ---- Regen backup codes ----

func TestTOTPRegenBackup_Success(t *testing.T) {
	f := newUnitFixture(t)
	secret, oldCodes := totpEnable(t, f)

	code, _ := totp.GenerateCode(secret, time.Now())
	resp := doUnit(t, f.app, "POST", "/api/totp/regen-backup", map[string]any{
		"code": code,
	}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		BackupCodes []string `json:"backupCodes"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.BackupCodes) == 0 {
		t.Fatal("expected new backup codes")
	}
	// New codes must differ from the old ones.
	if out.BackupCodes[0] == oldCodes[0] {
		t.Fatal("new backup codes should differ from old ones")
	}
}

func TestTOTPRegenBackup_WrongCode(t *testing.T) {
	f := newUnitFixture(t)
	totpEnable(t, f)

	resp := doUnit(t, f.app, "POST", "/api/totp/regen-backup", map[string]any{
		"code": "000000",
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestTOTPRegenBackup_WhenNotEnabled(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/api/totp/regen-backup", map[string]any{
		"code": "123456",
	}, f.userToken)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ---- Trusted devices ----

func TestTOTPTrustedDevice_SkipsTOTP(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpEnable(t, f)

	// First login with rememberDevice=true to get the cookie.
	code, _ := totp.GenerateCode(secret, time.Now())
	req, _ := newJSONRequest("POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
		"totpCode": code, "rememberDevice": true,
	})
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("first login: want 200, got %d: %s", resp.StatusCode, body)
	}
	cookie := extractCookie(resp, "mist_device")
	if cookie == "" {
		t.Fatal("expected mist_device cookie in response")
	}

	// Second login: password only + device cookie → should succeed without TOTP.
	req, _ = newJSONRequest("POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
	})
	req.Header.Set("Cookie", "mist_device="+cookie)
	resp, err = f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("cookie login: want 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestTOTPTrustedDevice_InvalidCookieStillRequiresTOTP(t *testing.T) {
	f := newUnitFixture(t)
	totpEnable(t, f)

	req, _ := newJSONRequest("POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
	})
	req.Header.Set("Cookie", "mist_device=fakeuuid:deadbeef")
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid cookie → falls through to TOTP challenge.
	if resp.StatusCode != 202 {
		t.Fatalf("want 202 totp_required, got %d", resp.StatusCode)
	}
}

func TestDeviceList_RequiresAuth(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "GET", "/api/devices", nil, "")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestDeviceRevoke_RemovesDevice(t *testing.T) {
	f := newUnitFixture(t)
	secret, _ := totpEnable(t, f)

	// Register a device.
	code, _ := totp.GenerateCode(secret, time.Now())
	req, _ := newJSONRequest("POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
		"totpCode": code, "rememberDevice": true,
	})
	resp, _ := f.app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Fatalf("login: want 200, got %d", resp.StatusCode)
	}

	// List devices: should have one.
	listResp := doUnit(t, f.app, "GET", "/api/devices", nil, f.userToken)
	var devices []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(listResp.Body).Decode(&devices)
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	// Revoke it.
	revokeResp := doUnit(t, f.app, "DELETE", "/api/devices/"+devices[0].ID, nil, f.userToken)
	if revokeResp.StatusCode != 200 {
		t.Fatalf("revoke: want 200, got %d", revokeResp.StatusCode)
	}

	// List again: should be empty.
	listResp = doUnit(t, f.app, "GET", "/api/devices", nil, f.userToken)
	devices = nil
	json.NewDecoder(listResp.Body).Decode(&devices)
	if len(devices) != 0 {
		t.Fatalf("expected 0 devices after revoke, got %d", len(devices))
	}
}

// ---- helpers ----

func newJSONRequest(method, path string, body any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func extractCookie(resp *http.Response, name string) string {
	for _, ck := range resp.Cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}
