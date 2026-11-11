// Package qa provides small, deterministic helpers for test automation.
package qa

import (
	"errors"
	"regexp"
)

var (
	ErrNoCode        = errors.New("no verification code found")
	ErrAmbiguousCode = errors.New("more than one verification code found")
	codePattern      = regexp.MustCompile(`(?i)(?:otp|one[- ]time(?: password| code)?|verification code|security code|passcode|رمز التحقق)[^0-9\r\n]{0,40}([0-9]{4,10})\b`)
)

// VerificationCode extracts a uniquely identified numeric verification code.
// Ambiguous messages are rejected instead of returning an arbitrary number.
func VerificationCode(text string) (string, error) {
	var code string
	for offset := 0; offset < len(text); {
		match := codePattern.FindStringSubmatchIndex(text[offset:])
		if match == nil {
			break
		}
		found := text[offset+match[2] : offset+match[3]]
		if code != "" && code != found {
			return "", ErrAmbiguousCode
		}
		code = found
		offset += match[1]
	}
	if code == "" {
		return "", ErrNoCode
	}
	return code, nil
}
