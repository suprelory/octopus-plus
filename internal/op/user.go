package op

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
)

const (
	AdminUsernameEnv = "OCTOPUS_ADMIN_USERNAME"
	AdminPasswordEnv = "OCTOPUS_ADMIN_PASSWORD"
	MinPasswordLen   = 12
	MaxPasswordBytes = 72
)

// userCache 被登录路径（UserVerify / UserGet）读、被改密改名路径写，必须加锁。
// 另外所有写路径都是「先写库成功再更新缓存」——反过来会在 DB 失败时把内存和
// 库改到不一致，例如改密失败后旧密码已经在内存里失效。
var (
	userCacheLock sync.RWMutex
	// userWriteLock serializes the complete DB-write/cache-commit sequence.
	// userCacheLock alone only protects individual memory accesses and cannot
	// prevent two writers from committing DB and cache updates in different orders.
	userWriteLock  sync.Mutex
	userCache      model.User
	bootstrapToken string
)

func UserInit() error {
	userWriteLock.Lock()
	defer userWriteLock.Unlock()

	var existing model.User
	if err := db.GetDB().First(&existing).Error; err == nil {
		setUserCache(existing)
		clearBootstrapToken()
		return nil
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return fmt.Errorf("load administrator: %w", err)
	}

	password, configured := os.LookupEnv(AdminPasswordEnv)
	if !configured || password == "" {
		clearUserCache()
		token, err := generateBootstrapToken()
		if err != nil {
			return fmt.Errorf("generate administrator bootstrap token: %w", err)
		}
		setBootstrapToken(token)
		log.Warnw("administrator.bootstrap_required",
			"environment", AdminPasswordEnv,
			"bootstrap_token", token,
			"message", "use this one-time token in the web initialization form")
		return nil
	}

	username := strings.TrimSpace(os.Getenv(AdminUsernameEnv))
	if username == "" {
		username = "admin"
	}
	initial, err := createInitialUser(username, password)
	if err != nil {
		return fmt.Errorf("initialize administrator from environment: %w", err)
	}
	clearBootstrapToken()
	log.Infow("administrator.initialized", "source", "environment", "username", initial.Username)
	return nil
}

func UserBootstrapRequired() (bool, error) {
	var count int64
	if err := db.GetDB().Model(&model.User{}).Count(&count).Error; err != nil {
		return false, fmt.Errorf("count administrators: %w", err)
	}
	return count == 0, nil
}

func UserBootstrap(username, password, token string) error {
	userWriteLock.Lock()
	defer userWriteLock.Unlock()

	required, err := UserBootstrapRequired()
	if err != nil {
		return err
	}
	if !required {
		return apperror.New(apperror.CodeAuthAlreadyInitialized, "administrator is already initialized").WithStatus(http.StatusConflict)
	}
	if !bootstrapTokenValid(token) {
		return apperror.New(apperror.CodeAuthBootstrapTokenInvalid, "invalid administrator bootstrap token").WithStatus(http.StatusUnauthorized)
	}

	if _, err := createInitialUser(username, password); err != nil {
		return err
	}
	clearBootstrapToken()
	log.Infow("administrator.initialized", "source", "web", "username", strings.TrimSpace(username))
	return nil
}

func generateBootstrapToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func bootstrapTokenValid(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	userCacheLock.RLock()
	expected := bootstrapToken
	userCacheLock.RUnlock()
	if expected == "" || len(candidate) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1
}

func setBootstrapToken(token string) {
	userCacheLock.Lock()
	bootstrapToken = token
	userCacheLock.Unlock()
}

func clearBootstrapToken() {
	setBootstrapToken("")
}

func createInitialUser(username, password string) (model.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return model.User{}, apperror.InvalidParam("username is required")
	}
	if err := validatePassword(password); err != nil {
		return model.User{}, err
	}

	initial := model.User{Username: username, Password: password}
	if err := initial.HashPassword(); err != nil {
		return model.User{}, err
	}
	if err := db.GetDB().Create(&initial).Error; err != nil {
		return model.User{}, fmt.Errorf("create administrator: %w", err)
	}
	setUserCache(initial)
	return initial, nil
}

func validatePassword(password string) error {
	if password == "" {
		return apperror.InvalidParam("password is required")
	}
	if len([]rune(password)) < MinPasswordLen {
		return apperror.InvalidParam(fmt.Sprintf("password must be at least %d characters", MinPasswordLen))
	}
	if len(password) > MaxPasswordBytes {
		return apperror.InvalidParam(fmt.Sprintf("password must not exceed %d bytes", MaxPasswordBytes))
	}
	return nil
}

func UserChangePassword(oldPassword, newPassword string) error {
	userWriteLock.Lock()
	defer userWriteLock.Unlock()

	current := userSnapshot()

	if err := current.ComparePassword(oldPassword); err != nil {
		return apperror.Wrap(apperror.CodeAuthPasswordIncorrect, "incorrect old password", err).WithStatus(http.StatusBadRequest)
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	updated := current
	updated.Password = newPassword
	if err := updated.HashPassword(); err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	if err := db.GetDB().Model(&model.User{}).Where("id = ?", current.ID).
		Update("password", updated.Password).Error; err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	setUserCache(updated)

	return nil
}

func UserChangeUsername(newUsername string) error {
	userWriteLock.Lock()
	defer userWriteLock.Unlock()

	current := userSnapshot()
	if current.Username == newUsername {
		return fmt.Errorf("new username is the same as the old username")
	}

	if err := db.GetDB().Model(&model.User{}).Where("id = ?", current.ID).
		Update("username", newUsername).Error; err != nil {
		return fmt.Errorf("failed to update username: %w", err)
	}

	userCacheLock.Lock()
	userCache.Username = newUsername
	userCacheLock.Unlock()

	return nil
}

func UserVerify(username, password string) error {
	_, err := UserVerifyAndGet(username, password)
	return err
}

func UserVerifyAndGet(username, password string) (model.User, error) {
	current := userSnapshot()
	if current.ID == 0 {
		return model.User{}, apperror.New(apperror.CodeAuthBootstrapRequired, "administrator setup is required").WithStatus(http.StatusServiceUnavailable)
	}
	if username != current.Username {
		return model.User{}, fmt.Errorf("incorrect username")
	}
	if err := current.ComparePassword(password); err != nil {
		return model.User{}, fmt.Errorf("incorrect password")
	}
	return current, nil
}

func UserGet() model.User {
	return userSnapshot()
}

// userSnapshot 返回 userCache 的值拷贝，调用方拿到后可以在锁外自由使用。
func userSnapshot() model.User {
	userCacheLock.RLock()
	defer userCacheLock.RUnlock()
	return userCache
}

func setUserCache(user model.User) {
	userCacheLock.Lock()
	if userCache.ID != user.ID || userCache.Password != user.Password {
		revokeRelayLogStreams()
	}
	userCache = user
	userCacheLock.Unlock()
}

func clearUserCache() {
	setUserCache(model.User{})
}
