package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	UID  string `json:"uid"`
	Role string `json:"role"`
	Ver  int64  `json:"ver,omitempty"`
	jwt.RegisteredClaims
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash is a valid bcrypt hash computed once at startup. It exists
// only so DummyVerify can burn a real bcrypt comparison.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)

// DummyVerify spends a bcrypt comparison against a throwaway hash. Call
// it on the unknown-user login path so the response latency matches the
// wrong-password path — denying an attacker a timing oracle for
// username enumeration. The result is intentionally discarded.
func DummyVerify(pw string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}

func Issue(secret, uid, role string, ver int64, ttl time.Duration) (string, error) {
	c := Claims{
		UID:  uid,
		Role: role,
		Ver:  ver,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
}

func Parse(secret, token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("bad alg")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*Claims)
	if !ok || !t.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}
