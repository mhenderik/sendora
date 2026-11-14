package app

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPathSegmentBytes = 200
	maxPathBytes        = 1024
	maxShareNameRunes   = 200
	maxDescriptionRunes = 2000
	maxMessageRunes     = 10000
)

// hasFormatRune reports invisible formatting characters (Unicode category Cf),
// which includes the bidirectional overrides (U+202A–202E, U+2066–2069, U+200E/F)
// that make "photo‮gpj.exe" display as an image. Only the zero-width
// (non-)joiners are allowed: emoji sequences and several scripts need them.
func hasFormatRune(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) && r != '‌' && r != '‍' {
			return true
		}
	}
	return false
}

// validateNewPath checks the shape of a path for NEW uploads (never applied to
// stored legacy names): bounded length and no invisible formatting characters.
func validateNewPath(p string) error {
	if len(p) > maxPathBytes || hasFormatRune(p) {
		return fmt.Errorf("unsafe file path")
	}
	for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		if len(seg) > maxPathSegmentBytes {
			return fmt.Errorf("file name too long")
		}
	}
	return nil
}

// validateText bounds a free-text field (counted in characters) and rejects
// invisible formatting characters when strict is set (used for names).
func validateText(s string, maxRunes int, strict bool) error {
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Errorf("text too long")
	}
	if strict && hasFormatRune(s) {
		return fmt.Errorf("invalid characters")
	}
	return nil
}
