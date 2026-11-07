package log

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/apperror"
)

func TestSafeErrorRetainsCauseWithoutCredentialValues(t *testing.T) {
	err := apperror.Wrap("channel.update_failed", "channel update failed", errors.New(
		`database unavailable password=database-secret; upstream https://user:proxy-secret@example.com/hook/webhook-secret?token=query-secret returned {"token":"json-secret"}; key sk-private-secret`))
	message := SafeError(err)
	for _, want := range []string{"channel update failed", "database unavailable", "example.com"} {
		if !strings.Contains(message, want) {
			t.Errorf("missing %q in %q", want, message)
		}
	}
	for _, secret := range []string{"database-secret", "proxy-secret", "webhook-secret", "query-secret", "json-secret", "sk-private-secret"} {
		if strings.Contains(message, secret) {
			t.Errorf("credential leaked: %s", secret)
		}
	}
}

func TestSafeErrorRespectsDomainSanitization(t *testing.T) {
	sanitized := apperror.Wrap("site.failed", "upstream rejected credentials", errors.New("opaque-account-credential")).WithLogMessage("upstream rejected credentials")
	err := apperror.Wrap("site.project_failed", "site project failed", sanitized)
	message := SafeError(err)
	if strings.Contains(message, "opaque-account-credential") || !strings.Contains(message, "upstream rejected credentials") {
		t.Fatalf("unsafe diagnostic: %q", message)
	}
}

func TestSafeErrorRespectsEmbeddedDomainSanitization(t *testing.T) {
	sanitized := apperror.New("site.failed", "invalid opaque-account-credential").WithLogMessage("upstream rejected credentials")
	for _, err := range []error{
		fmt.Errorf("sync failed: %w", sanitized),
		errors.Join(errors.New("database unavailable"), fmt.Errorf("sync failed: %w", sanitized)),
	} {
		message := SafeError(err)
		if strings.Contains(message, "opaque-account-credential") || !strings.Contains(message, "upstream rejected credentials") {
			t.Fatalf("unsafe diagnostic: %q", message)
		}
	}
}

func TestSafeTextRedactsHeadersAndSQLLiterals(t *testing.T) {
	for _, value := range []string{
		"Authorization: Bearer opaque-credential",
		"Cookie: session=opaque-credential; other=second-credential",
		"request failed with Basic opaque-credential",
		"INSERT INTO keys VALUES ('opaque-credential') failed",
	} {
		if message := SafeText(value); strings.Contains(message, "opaque-credential") || strings.Contains(message, "second-credential") {
			t.Fatalf("unsafe diagnostic: %q", message)
		}
	}
}
