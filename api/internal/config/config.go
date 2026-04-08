package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port             string
	JWTSecret        string
	JWTTTL           time.Duration
	AdminLogin       string
	AdminPassword    string
	DataDir          string
	S3Endpoint       string
	S3AccessKey      string
	S3SecretKey      string
	S3UseSSL         bool
	PublicS3Host     string // host clients use for presigned URLs
	UploadTTL        time.Duration
	DefaultQuota     int64
	CORSOrigins      string
	LogLevel         string
	LogPath          string
	ServiceName      string
	PresignDownload  time.Duration
	MaxZipBytes      int64
}

// minJWTSecretLen is the shortest JWT secret we accept.
// 32 bytes = 256 bits, matching HS256's output size. Anything less
// meaningfully weakens HMAC-SHA256. Generate with: openssl rand -base64 48
const minJWTSecretLen = 32

func Load() *Config {
	secret := must("JWT_SECRET")
	if len(secret) < minJWTSecretLen {
		log.Fatalf("JWT_SECRET must be at least %d characters (got %d). Generate one with: openssl rand -base64 48", minJWTSecretLen, len(secret))
	}
	c := &Config{
		Port:            env("PORT", "3000"),
		JWTSecret:       secret,
		JWTTTL:          duration("JWT_TTL", 24*time.Hour),
		AdminLogin:      env("ADMIN_LOGIN", "admin"),
		AdminPassword:   must("ADMIN_PASSWORD"),
		DataDir:         env("DATA_DIR", "/data"),
		S3Endpoint:      must("S3_ENDPOINT"),
		S3AccessKey:     must("S3_ACCESS_KEY"),
		S3SecretKey:     must("S3_SECRET_KEY"),
		S3UseSSL:        env("S3_USE_SSL", "false") == "true",
		PublicS3Host:    env("PUBLIC_S3_HOST", ""),
		UploadTTL:       time.Duration(intEnv("UPLOAD_TTL_HOURS", 6)) * time.Hour,
		DefaultQuota:    int64(intEnv("DEFAULT_QUOTA_BYTES", 10*1024*1024*1024)),
		CORSOrigins:     env("CORS_ORIGINS", "*"),
		LogLevel:        env("LOG_LEVEL", "info"),
		LogPath:         env("LOG_PATH", "/app/logs/app.log"),
		ServiceName:     env("SERVICE_NAME", "mist-drive-api"),
		PresignDownload: duration("PRESIGN_DOWNLOAD_TTL", 5*time.Minute),
		MaxZipBytes:     int64(intEnv("MAX_ZIP_BYTES", 20*1024*1024*1024)), // 20 GiB
	}
	return c
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing required env %s", k)
	}
	return v
}
func intEnv(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}
func duration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if x, err := time.ParseDuration(v); err == nil {
			return x
		}
	}
	return d
}
