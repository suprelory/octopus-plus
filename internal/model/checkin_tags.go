package model

import (
	"fmt"
	"unicode/utf8"
)

const (
	SiteTagMaxLength = 32
	SiteTagsMaxCount = 20
)

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
