package model

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const CheckinRewardExtractorMaxBytes = 16 << 10

func ValidateCheckinRewardExtractor(code string) error {
	if len(code) > CheckinRewardExtractorMaxBytes {
		return fmt.Errorf("checkin reward extractor must not exceed 16 KiB")
	}
	if !utf8.ValidString(code) || strings.ContainsRune(code, '\x00') {
		return fmt.Errorf("checkin reward extractor must be valid UTF-8 without null bytes")
	}
	return nil
}
