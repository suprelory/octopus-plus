package op

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

const testAdminPassword = "initial-password"

func initTestUser(t *testing.T) {
	t.Helper()
	t.Setenv(AdminUsernameEnv, "admin")
	t.Setenv(AdminPasswordEnv, testAdminPassword)
	if err := UserInit(); err != nil {
		t.Fatalf("UserInit failed: %v", err)
	}
}

func TestUserInitWithoutEnvironmentRequiresBootstrap(t *testing.T) {
	setupSiteOpTestDB(t)
	t.Setenv(AdminUsernameEnv, "")
	if err := os.Unsetenv(AdminPasswordEnv); err != nil {
		t.Fatalf("Unsetenv failed: %v", err)
	}

	if err := UserInit(); err != nil {
		t.Fatalf("UserInit failed: %v", err)
	}
	required, err := UserBootstrapRequired()
	if err != nil {
		t.Fatalf("UserBootstrapRequired failed: %v", err)
	}
	if !required {
		t.Fatal("fresh installation did not require administrator bootstrap")
	}
	if got := UserGet(); got.ID != 0 {
		t.Fatalf("fresh installation unexpectedly created user %+v", got)
	}
	if bootstrapToken == "" {
		t.Fatal("fresh installation did not create a one-time bootstrap token")
	}
	if err := UserVerify("admin", "admin"); !apperror.IsCode(err, apperror.CodeAuthBootstrapRequired) {
		t.Fatalf("UserVerify error = %v, want bootstrap required", err)
	}
}

func TestUserInitFromEnvironmentAndVerify(t *testing.T) {
	setupSiteOpTestDB(t)

	secret := "environment-secret"
	t.Setenv(AdminUsernameEnv, "admin")
	t.Setenv(AdminPasswordEnv, secret)
	core, observed := observer.New(zap.InfoLevel)
	originalLogger := log.Logger
	log.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { log.Logger = originalLogger })

	if err := UserInit(); err != nil {
		t.Fatalf("UserInit failed: %v", err)
	}

	if err := UserVerify("admin", secret); err != nil {
		t.Fatalf("UserVerify with the initial credentials failed: %v", err)
	}
	if err := UserVerify("admin", "wrong"); err == nil {
		t.Fatal("UserVerify accepted a wrong password")
	}
	if err := UserVerify("nobody", secret); err == nil {
		t.Fatal("UserVerify accepted a wrong username")
	}

	user := UserGet()
	if user.ID == 0 {
		t.Fatal("the initial user was created without a primary key")
	}
	if user.Password == secret {
		t.Fatal("the initial password was stored in plaintext")
	}
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message+fmt.Sprint(entry.Context), secret) {
			t.Fatal("initial administrator password was written to logs")
		}
	}

	// Existing credentials win on repeated startup, even if the environment is removed.
	if err := os.Unsetenv(AdminPasswordEnv); err != nil {
		t.Fatalf("Unsetenv failed: %v", err)
	}
	if err := UserInit(); err != nil {
		t.Fatalf("second UserInit failed: %v", err)
	}
	if err := UserVerify("admin", secret); err != nil {
		t.Fatalf("repeated startup changed the administrator credentials: %v", err)
	}
}

func TestUserBootstrapOnce(t *testing.T) {
	setupSiteOpTestDB(t)
	t.Setenv(AdminPasswordEnv, "")
	if err := os.Unsetenv(AdminPasswordEnv); err != nil {
		t.Fatalf("Unsetenv failed: %v", err)
	}
	if err := UserInit(); err != nil {
		t.Fatalf("UserInit failed: %v", err)
	}

	token := bootstrapToken
	if err := UserBootstrap("operator", "bootstrap-secret", "wrong-token"); !apperror.IsCode(err, apperror.CodeAuthBootstrapTokenInvalid) {
		t.Fatalf("UserBootstrap with wrong token error = %v, want invalid token", err)
	}
	if err := UserBootstrap("operator", "bootstrap-secret", token); err != nil {
		t.Fatalf("UserBootstrap failed: %v", err)
	}
	if err := UserVerify("operator", "bootstrap-secret"); err != nil {
		t.Fatalf("bootstrapped credentials failed: %v", err)
	}
	if bootstrapToken != "" {
		t.Fatal("successful bootstrap did not consume the one-time token")
	}
	if err := UserBootstrap("attacker", "another-secret", token); !apperror.IsCode(err, apperror.CodeAuthAlreadyInitialized) {
		t.Fatalf("second UserBootstrap error = %v, want already initialized", err)
	}
}

func TestUserInitRotatesBootstrapTokenOnRestart(t *testing.T) {
	setupSiteOpTestDB(t)
	if err := os.Unsetenv(AdminPasswordEnv); err != nil {
		t.Fatalf("Unsetenv failed: %v", err)
	}
	if err := UserInit(); err != nil {
		t.Fatalf("first UserInit failed: %v", err)
	}
	first := bootstrapToken
	if first == "" {
		t.Fatal("first UserInit did not create a bootstrap token")
	}
	if err := UserInit(); err != nil {
		t.Fatalf("second UserInit failed: %v", err)
	}
	second := bootstrapToken
	if second == "" || second == first {
		t.Fatalf("bootstrap token was not rotated across restart: first=%q second=%q", first, second)
	}
}

func TestUserInitTreatsEmptyEnvironmentPasswordAsBootstrap(t *testing.T) {
	setupSiteOpTestDB(t)
	t.Setenv(AdminPasswordEnv, "")
	if err := UserInit(); err != nil {
		t.Fatalf("UserInit with empty environment password failed: %v", err)
	}
	if bootstrapToken == "" {
		t.Fatal("empty environment password did not produce a bootstrap token")
	}
	if got := UserGet(); got.ID != 0 {
		t.Fatalf("empty environment password unexpectedly created user %+v", got)
	}
}

func TestUserInitRejectsWeakEnvironmentPassword(t *testing.T) {
	setupSiteOpTestDB(t)
	t.Setenv(AdminPasswordEnv, "short")
	if err := UserInit(); err == nil {
		t.Fatal("UserInit accepted a weak environment password")
	}
	required, err := UserBootstrapRequired()
	if err != nil {
		t.Fatalf("UserBootstrapRequired failed: %v", err)
	}
	if !required {
		t.Fatal("weak environment password created an administrator")
	}
}

func TestUserInitRejectsEnvironmentPasswordOverBcryptLimit(t *testing.T) {
	setupSiteOpTestDB(t)
	t.Setenv(AdminPasswordEnv, strings.Repeat("😀", 19))

	if err := UserInit(); !apperror.IsCode(err, apperror.CodeCommonInvalidParam) {
		t.Fatalf("UserInit error = %v, want invalid parameter", err)
	}
	required, err := UserBootstrapRequired()
	if err != nil {
		t.Fatalf("UserBootstrapRequired failed: %v", err)
	}
	if !required {
		t.Fatal("overlong environment password created an administrator")
	}
	if got := UserGet(); got.ID != 0 {
		t.Fatalf("overlong environment password created user %+v", got)
	}
}

func TestUserBootstrapRejectsPasswordOverBcryptLimit(t *testing.T) {
	setupSiteOpTestDB(t)
	if err := os.Unsetenv(AdminPasswordEnv); err != nil {
		t.Fatalf("Unsetenv failed: %v", err)
	}
	if err := UserInit(); err != nil {
		t.Fatalf("UserInit failed: %v", err)
	}

	token := bootstrapToken
	if err := UserBootstrap("operator", strings.Repeat("😀", 19), token); !apperror.IsCode(err, apperror.CodeCommonInvalidParam) {
		t.Fatalf("UserBootstrap error = %v, want invalid parameter", err)
	}
	if got := UserGet(); got.ID != 0 {
		t.Fatalf("overlong bootstrap password created user %+v", got)
	}
	if bootstrapToken != token {
		t.Fatal("rejected bootstrap password consumed the one-time token")
	}
}

func TestUserChangePasswordPersists(t *testing.T) {
	setupSiteOpTestDB(t)

	initTestUser(t)

	if err := UserChangePassword("wrong-old", "new-password"); err == nil {
		t.Fatal("UserChangePassword accepted a wrong old password")
	}
	// 旧密码校验失败后缓存不能被改动。
	if err := UserVerify("admin", testAdminPassword); err != nil {
		t.Fatalf("a rejected password change invalidated the old password: %v", err)
	}

	if err := UserChangePassword(testAdminPassword, "new-password"); err != nil {
		t.Fatalf("UserChangePassword failed: %v", err)
	}
	if err := UserVerify("admin", "new-password"); err != nil {
		t.Fatalf("the new password does not verify: %v", err)
	}
	if err := UserVerify("admin", testAdminPassword); err == nil {
		t.Fatal("the old password still verifies after a successful change")
	}
}

func TestUserChangePasswordRejectsPasswordOverBcryptLimit(t *testing.T) {
	setupSiteOpTestDB(t)
	initTestUser(t)

	if err := UserChangePassword(testAdminPassword, strings.Repeat("😀", 19)); !apperror.IsCode(err, apperror.CodeCommonInvalidParam) {
		t.Fatalf("UserChangePassword error = %v, want invalid parameter", err)
	}
	if err := UserVerify("admin", testAdminPassword); err != nil {
		t.Fatalf("rejected password change invalidated the old password: %v", err)
	}
}

func TestUserChangeUsernamePersists(t *testing.T) {
	setupSiteOpTestDB(t)

	initTestUser(t)

	if err := UserChangeUsername("admin"); err == nil {
		t.Fatal("UserChangeUsername accepted the current username")
	}
	if err := UserChangeUsername("operator"); err != nil {
		t.Fatalf("UserChangeUsername failed: %v", err)
	}
	if got := UserGet().Username; got != "operator" {
		t.Fatalf("username after change = %q, want %q", got, "operator")
	}
	if err := UserVerify("operator", testAdminPassword); err != nil {
		t.Fatalf("verify with the new username failed: %v", err)
	}
}

// userCache 被登录路径读、被改密改名路径写。跑 `go test -race ./internal/op/...`
// 时这个用例会抓到未加锁的访问。
func TestUserCacheConcurrentReadWrite(t *testing.T) {
	setupSiteOpTestDB(t)

	initTestUser(t)

	const writes = 20

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = UserGet()
			}
		}()
	}

	// bcrypt 很慢，读侧只留一个 goroutine 走完整的校验路径。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = UserVerify("admin", testAdminPassword)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < writes; i++ {
			if err := UserChangeUsername(fmt.Sprintf("admin-%d", i)); err != nil {
				t.Errorf("UserChangeUsername(%d) failed: %v", i, err)
				return
			}
		}
	}()

	wg.Wait()

	if got := UserGet().Username; got != fmt.Sprintf("admin-%d", writes-1) {
		t.Fatalf("username after concurrent writes = %q, want %q", got, fmt.Sprintf("admin-%d", writes-1))
	}
}

func TestUserWritersSerializeDBAndCacheCommit(t *testing.T) {
	setupSiteOpTestDB(t)
	initTestUser(t)

	const callbackName = "test:user-writer-serialization"
	entered := make(chan int, 2)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	database := dbpkg.GetDB()
	err := database.Callback().Update().Before("gorm:update").Register(callbackName, func(_ *gorm.DB) {
		call := int(calls.Add(1))
		entered <- call
		if call == 1 {
			<-releaseFirst
		}
	})
	if err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	t.Cleanup(func() { _ = database.Callback().Update().Remove(callbackName) })

	errs := make(chan error, 2)
	go func() { errs <- UserChangeUsername("operator-a") }()
	select {
	case call := <-entered:
		if call != 1 {
			t.Fatalf("first callback call = %d, want 1", call)
		}
	case <-time.After(time.Second):
		t.Fatal("first writer did not reach the DB update callback")
	}

	go func() { errs <- UserChangeUsername("operator-b") }()
	secondEnteredEarly := false
	select {
	case <-entered:
		secondEnteredEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if secondEnteredEarly {
		t.Fatal("second writer crossed the DB update boundary before the first committed")
	}

	select {
	case call := <-entered:
		if call != 2 {
			t.Fatalf("second callback call = %d, want 2", call)
		}
	case <-time.After(time.Second):
		t.Fatal("second writer did not resume after the first committed")
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("UserChangeUsername failed: %v", err)
		}
	}

	var stored model.User
	if err := database.First(&stored).Error; err != nil {
		t.Fatalf("load stored user: %v", err)
	}
	if got := UserGet().Username; got != stored.Username || got != "operator-b" {
		t.Fatalf("cache username %q and DB username %q diverged", got, stored.Username)
	}
}
