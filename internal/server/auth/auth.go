package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

var (
	jwtSecretOnce sync.Once
	jwtSecretKey  []byte
)

// getJWTSecret returns the JWT signing key, generating and persisting one if needed.
func getJWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		secret, err := op.SettingGetString(model.SettingKeyJWTSecret)
		if err == nil && secret != "" {
			jwtSecretKey = []byte(secret)
			return
		}

		// Generate a random 32-byte secret
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		generated := base64.RawURLEncoding.EncodeToString(b)
		if err := op.SettingSetString(model.SettingKeyJWTSecret, generated); err != nil {
			// If we can't persist, still use the generated key for this session
			log.Warnf("failed to persist JWT secret: %s", err.Error())
		}
		jwtSecretKey = []byte(generated)
	})
	return jwtSecretKey
}

// JWT 有效期约定（分钟）：
//   - expire == 0：默认短有效期
//   - expire == -1：30 天可续期令牌，单次登录最长 90 天
//   - expire > 0：客户端指定，但会被 clamp 到 MaxJWTExpiresMin
//
// 其它取值由 handler 侧拒绝；jwtLifetime 仍然兜底到默认值，保证 ExpiresAt
// 一定非 nil（历史实现里 expire < -1 三个分支都不命中，会签出没有 exp 声明的
// token，并在随后的 claims.ExpiresAt.Format 上空指针 panic）。
const (
	defaultJWTExpiresMin    = 15
	rememberMeJWTExpiresMin = 30 * 24 * 60
	// MaxJWTExpiresMin 是客户端可请求的最长有效期（30 天）。
	MaxJWTExpiresMin   = 30 * 24 * 60
	JWTSessionLifetime = 90 * 24 * time.Hour
	JWTRenewalWindow   = 7 * 24 * time.Hour
)

var (
	ErrInvalidToken = errors.New("invalid administrator token")
	ErrExpiredToken = errors.New("administrator token has expired")
)

// SessionClaims keeps the original authentication time and identity across
// renewals. Legacy tokens have none of the four session fields.
type SessionClaims struct {
	jwt.RegisteredClaims
	Version   int    `json:"ver,omitempty"`
	SessionID string `json:"sid,omitempty"`
	AuthTime  int64  `json:"auth_time,omitempty"`
	Renewable bool   `json:"renewable"`
	legacy    bool
}

func (c *SessionClaims) UnmarshalJSON(data []byte) error {
	type claimsAlias SessionClaims
	var decoded claimsAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	// encoding/json matches struct fields without case sensitivity, whereas JWT
	// claim names are case-sensitive. Do not mistake a differently-cased session
	// claim for a legacy token, or accept quoted NumericDate values.
	for key, value := range fields {
		switch strings.ToLower(key) {
		case "ver", "sid", "auth_time", "renewable", "iss", "iat", "nbf", "exp":
			if key != strings.ToLower(key) {
				return ErrInvalidToken
			}
		}
		if (key == "iat" || key == "nbf" || key == "exp") && string(value) != "null" {
			var number float64
			if err := json.Unmarshal(value, &number); err != nil {
				return ErrInvalidToken
			}
		}
	}
	count := 0
	for _, key := range []string{"ver", "sid", "auth_time", "renewable"} {
		if value, exists := fields[key]; exists {
			if string(value) == "null" {
				return ErrInvalidToken
			}
			count++
		}
	}
	if count != 0 && count != 4 {
		return ErrInvalidToken
	}
	*c = SessionClaims(decoded)
	c.legacy = count == 0
	return nil
}

func (c *SessionClaims) validateSession(now time.Time) error {
	if c.legacy {
		return nil
	}
	if c.Version != 2 || c.SessionID == "" || c.AuthTime <= 0 ||
		c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil {
		return ErrInvalidToken
	}
	authTime := time.Unix(c.AuthTime, 0)
	deadline := authTime.Add(JWTSessionLifetime)
	if authTime.After(c.IssuedAt.Time) || c.IssuedAt.After(now) ||
		c.NotBefore.Before(c.IssuedAt.Time) || !c.ExpiresAt.After(c.NotBefore.Time) ||
		c.ExpiresAt.Sub(c.IssuedAt.Time) > jwtLifetime(-1) || c.ExpiresAt.After(deadline) {
		return ErrInvalidToken
	}
	if !now.Before(deadline) {
		return ErrExpiredToken
	}
	return nil
}

// JWTExpireValid 判断客户端传来的 expire 是否在白名单内。正数超过上限不算
// 非法，会在 jwtLifetime 里被 clamp。
func JWTExpireValid(expiresMin int) bool {
	return expiresMin >= -1
}

// jwtLifetime 把 expire 映射成有效期，任何不认识的取值都落到默认值。
func jwtLifetime(expiresMin int) time.Duration {
	switch {
	case expiresMin > 0:
		if expiresMin > MaxJWTExpiresMin {
			expiresMin = MaxJWTExpiresMin
		}
		return time.Duration(expiresMin) * time.Minute
	case expiresMin == -1:
		return time.Duration(rememberMeJWTExpiresMin) * time.Minute
	default:
		return time.Duration(defaultJWTExpiresMin) * time.Minute
	}
}

func GenerateJWTToken(expiresMin int) (string, string, error) {
	return GenerateJWTTokenForUser(expiresMin, op.UserGet())
}

// Bind the signing key to the persisted password hash. A successful password
// change revokes existing sessions, including after a server restart.
func userJWTSecret(user model.User) []byte {
	mac := hmac.New(sha256.New, getJWTSecret())
	fmt.Fprintf(mac, "admin-session:%d:%s", user.ID, user.Password)
	return mac.Sum(nil)
}

// GenerateJWTTokenForUser signs for the same snapshot whose credentials were
// verified, so a concurrent password change cannot mint a new valid session.
func GenerateJWTTokenForUser(expiresMin int, user model.User) (string, string, error) {
	return generateJWTTokenForUserAt(expiresMin, user, time.Now())
}

func generateJWTTokenForUserAt(expiresMin int, user model.User, now time.Time) (string, string, error) {
	if user.ID == 0 || user.Password == "" {
		return "", "", fmt.Errorf("administrator is not initialized")
	}
	var sessionID [24]byte
	if _, err := rand.Read(sessionID[:]); err != nil {
		return "", "", err
	}
	claims := &SessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    conf.APP_NAME,
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime(expiresMin))),
		},
		Version: 2, SessionID: base64.RawURLEncoding.EncodeToString(sessionID[:]),
		AuthTime: now.Unix(), Renewable: expiresMin == -1,
	}
	return signJWTTokenForUser(claims, user)
}

func signJWTTokenForUser(claims *SessionClaims, user model.User) (string, string, error) {
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(userJWTSecret(user))
	if err != nil {
		return "", "", err
	}
	return token, claims.ExpiresAt.Format(time.RFC3339), nil
}

func VerifyJWTToken(token string) bool {
	return VerifyJWTTokenForUser(token, op.UserGet())
}

// VerifyJWTTokenForUser lets middleware retain the exact authenticated
// credential snapshot for operations that create another kind of session.
func VerifyJWTTokenForUser(token string, user model.User) bool {
	_, err := ParseJWTTokenForUser(token, user)
	return err == nil
}

// ParseJWTTokenForUser validates against one immutable credential snapshot.
func ParseJWTTokenForUser(token string, user model.User) (*SessionClaims, error) {
	return parseJWTTokenForUserAt(token, user, time.Now())
}

func parseJWTTokenForUserAt(token string, user model.User, now time.Time) (*SessionClaims, error) {
	if user.ID == 0 || user.Password == "" {
		return nil, ErrInvalidToken
	}
	// 固定签名算法与签发者。golang-jwt v5 本身已拒绝 alg:none，且 keyfunc 返回
	// []byte 时 RS256 会因密钥类型不匹配而失败，这里属于纵深防御。
	claims := &SessionClaims{}
	jwtToken, err := jwt.ParseWithClaims(token, claims,
		func(token *jwt.Token) (interface{}, error) {
			return userJWTSecret(user), nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(conf.APP_NAME),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid) &&
			!errors.Is(err, jwt.ErrTokenInvalidIssuer) && !errors.Is(err, jwt.ErrTokenNotValidYet) &&
			!errors.Is(err, jwt.ErrTokenRequiredClaimMissing) &&
			claims.validateSession(now) != ErrInvalidToken {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if jwtToken == nil || !jwtToken.Valid {
		return nil, ErrInvalidToken
	}
	if err := claims.validateSession(now); err != nil {
		return nil, err
	}
	return claims, nil
}

// RenewJWTTokenForUser only accepts claims already verified with this user
// snapshot. Never reload credentials here: that would undo password revocation.
// An empty token means this still-valid session does not need renewal.
func RenewJWTTokenForUser(claims *SessionClaims, user model.User) (string, string, error) {
	return renewJWTTokenForUserAt(claims, user, time.Now())
}

func renewJWTTokenForUserAt(claims *SessionClaims, user model.User, now time.Time) (string, string, error) {
	if claims == nil || claims.ExpiresAt == nil || user.ID == 0 || user.Password == "" {
		return "", "", ErrInvalidToken
	}
	if err := claims.validateSession(now); err != nil {
		return "", "", err
	}
	if !now.Before(claims.ExpiresAt.Time) {
		return "", "", ErrExpiredToken
	}
	if !claims.Renewable || claims.ExpiresAt.Sub(now) >= JWTRenewalWindow {
		return "", "", nil
	}
	expiresAt := now.Add(jwtLifetime(-1))
	if deadline := time.Unix(claims.AuthTime, 0).Add(JWTSessionLifetime); expiresAt.After(deadline) {
		expiresAt = deadline
	}
	if !expiresAt.After(claims.ExpiresAt.Time) {
		return "", "", nil
	}
	renewed := *claims
	renewed.IssuedAt = jwt.NewNumericDate(now)
	renewed.NotBefore = jwt.NewNumericDate(now)
	renewed.ExpiresAt = jwt.NewNumericDate(expiresAt)
	return signJWTTokenForUser(&renewed, user)
}

func GenerateAPIKey() string {
	const keyChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const keyLen = 48
	// 字符集长度 62 不是 2 的幂，直接对随机字节取模会让前 8 个字符出现概率偏高，
	// 所以做拒绝采样：只接受 < 248 (= 62*4) 的字节，丢弃率约 3%。
	const maxAcceptable = 256 - 256%len(keyChars)

	out := make([]byte, 0, keyLen)
	// 一次多读一些，正常情况下一轮就够；不够再补读，避免 48 次系统调用。
	buf := make([]byte, keyLen+keyLen/8+8)
	for len(out) < keyLen {
		if _, err := rand.Read(buf); err != nil {
			return ""
		}
		for _, v := range buf {
			if int(v) >= maxAcceptable {
				continue
			}
			out = append(out, keyChars[int(v)%len(keyChars)])
			if len(out) == keyLen {
				break
			}
		}
	}
	return "sk-" + conf.APP_NAME + "-" + string(out)
}
