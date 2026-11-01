package config

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateWebUIPassword validates the secret without normalizing or logging it.
// Keep the exact bytes to match what HTTP Basic Auth sends.
func ValidateWebUIPassword(password string) error {
	if n := len(password); n < 12 || n > 256 {
		return errors.New("WebUI password must be 12 to 256 bytes")
	}
	if !utf8.ValidString(password) || strings.TrimSpace(password) != password {
		return errors.New("WebUI password must have valid UTF-8 and no surrounding whitespace")
	}
	for _, r := range password {
		if unicode.IsControl(r) {
			return errors.New("WebUI password cannot contain control characters")
		}
	}
	return nil
}
