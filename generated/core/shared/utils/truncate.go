package utils

import (
	"strings"
	"unicode/utf8"
)

// Caps for repo-derived strings echoed to the panel.
const (
	MaxLabelRunes = 256  // versions, blueprint/group names, module names
	MaxPathRunes  = 1024 // file paths
)

// TruncateRunes returns s cut to at most max runes, as valid UTF-8 (invalid bytes
// become U+FFFD). Repo content is attacker-controlled; this bounds what a record
// can carry to the panel and keeps the proto string field marshalable.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
