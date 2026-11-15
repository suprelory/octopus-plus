package log

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	logURLPattern    = regexp.MustCompile(`(?i)\b(?:https?|socks(?:5h?)?|postgres(?:ql)?|mysql|redis)://[^\s<>"']+`)
	logAuthPattern   = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[^\s,"';]+`)
	logHeaderPattern = regexp.MustCompile(`(?im)\b(?:authorization|cookie|set-cookie)\s*[:=][^\r\n]*`)
	logSecretPattern = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret)\s*["']?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s&,;]+)`)
	logKeyPattern    = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+`)
	logQuotedPattern = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:''|[^'])*'`)
)

// SafeText is for diagnostic messages, never request/response bodies. Keep
// endpoint hosts, but remove URL credentials, paths, queries, authorization
// values and quoted data (including SQL literals and upstream JSON values).
func SafeText(message string) string {
	if len(message) > 32*1024 {
		message = message[:32*1024] + "…"
	}
	message = logURLPattern.ReplaceAllStringFunc(message, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return "[redacted URL]"
		}
		return parsed.Scheme + "://" + parsed.Host + "/[redacted]"
	})
	message = logAuthPattern.ReplaceAllString(message, "[redacted authorization]")
	message = logHeaderPattern.ReplaceAllString(message, "[redacted header]")
	message = logSecretPattern.ReplaceAllString(message, "[redacted credential]")
	message = logKeyPattern.ReplaceAllString(message, "[redacted key]")
	message = logQuotedPattern.ReplaceAllString(message, "[redacted value]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 1024 {
		message = string(runes[:1024]) + "…"
	}
	return message
}

// SafeError includes wrapped causes, which application errors intentionally
// hide from clients. Bound traversal, including joined errors, before logging.
func SafeError(err error) string {
	remaining := 8
	var visit func(error) string
	visit = func(current error) string {
		if current == nil {
			return ""
		}
		if remaining == 0 {
			return "error chain truncated"
		}
		remaining--
		if safe, ok := current.(interface{ SafeLogMessage() (string, bool) }); ok {
			if message, available := safe.SafeLogMessage(); available {
				return SafeText(message)
			}
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			// Joined errors embed every child in Error(). Render only bounded,
			// sanitized children so a nested boundary cannot be bypassed.
			var messages []string
			for _, cause := range wrapped.Unwrap() {
				if remaining == 0 {
					messages = append(messages, "error chain truncated")
					break
				}
				if message := visit(cause); message != "" {
					messages = append(messages, message)
				}
			}
			return strings.Join(messages, "; ")
		case interface{ Unwrap() error }:
			if cause := wrapped.Unwrap(); cause != nil {
				diagnostic := visit(cause)
				message := current.Error()
				// fmt.Errorf embeds the child's public message. Replace that
				// before sanitizing the wrapper to honor domain redaction.
				if original := cause.Error(); original != "" {
					message = strings.ReplaceAll(message, original, diagnostic)
				}
				message = SafeText(message)
				if diagnostic != "" && !strings.Contains(message, diagnostic) {
					message += "; caused by: " + diagnostic
				}
				return message
			}
		}
		return SafeText(current.Error())
	}
	return visit(err)
}
