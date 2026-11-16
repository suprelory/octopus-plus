package rules

import (
	"regexp"
	"strings"
)

var (
	alreadyEnglish  = regexp.MustCompile(`(?i)\b(?:already\s+(?:been\s+)?(?:checked[ -]in|signed[ -]in)|(?:checked[ -]in|signed[ -]in)\s+(?:already|today))\b`)
	alreadyChinese  = regexp.MustCompile(`(?:已(?:经)?签到|签到过(?:了)?)(?:$|[了，。！!\s]|无法重复|不能重复)`)
	negativeCheckin = regexp.MustCompile(`(?i)\b(?:not|never|haven't|hasn't|didn't)\s+(?:(?:yet|already)\s+)?(?:checked[ -]in|signed[ -]in)\b|(?:未|没|没有)(?:曾经|曾|有|进行|完成)?签到`)
)

func AlreadyCheckedIn(message string) bool {
	trimmed := strings.TrimSpace(message)
	return trimmed != "" && !negativeCheckin.MatchString(trimmed) && (alreadyEnglish.MatchString(trimmed) || alreadyChinese.MatchString(trimmed))
}
