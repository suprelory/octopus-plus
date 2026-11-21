package model

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	SiteTagMaxLength = 32
	SiteTagsMaxCount = 20
)

func NormalizeSiteTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func ValidateSiteTags(tags []string) error {
	if len(tags) > SiteTagsMaxCount {
		return fmt.Errorf("site tags must not exceed %d", SiteTagsMaxCount)
	}
	for _, tag := range tags {
		if utf8.RuneCountInString(tag) > SiteTagMaxLength {
			return fmt.Errorf("site tag %q must not exceed %d characters", tag, SiteTagMaxLength)
		}
	}
	return nil
}
