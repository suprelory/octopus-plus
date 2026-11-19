package op

import (
	"errors"

	"gorm.io/gorm"
)

var (
	ErrCheckinConfiguration = errors.New("invalid check-in configuration")
	ErrCheckinNameConflict  = errors.New("check-in name is already in use")
	ErrCheckinProxyDisabled = errors.New("check-in proxy is disabled")
)

// Validation is tagged at the boundary that knows its origin. Storage failures
// must never be reported as client validation errors or expose SQL/secrets.
func checkinConfigurationError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrCheckinConfiguration, err)
}

// Only site/account configuration writes use this translation. Workflow log ID
// allocation has its own retry behavior and must retain its original errors.
func (s *CheckinStore) checkinNameError(err error) error {
	if err == nil {
		return nil
	}
	translated := err
	if translator, ok := s.conn.Dialector.(gorm.ErrorTranslator); ok {
		translated = translator.Translate(err)
	}
	var sqliteError interface{ Code() int }
	if errors.Is(translated, gorm.ErrDuplicatedKey) || errors.As(err, &sqliteError) && (sqliteError.Code() == 2067 || sqliteError.Code() == 1555) {
		return ErrCheckinNameConflict
	}
	return err
}
