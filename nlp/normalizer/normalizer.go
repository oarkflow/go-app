package normalizer

import (
	"strings"
	"unicode"
	
	"golang.org/x/text/unicode/norm"
)

// ToLowerASCII lowercases ASCII letters.
func ToLowerASCII(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = strings.ToLower(t)
	}
	return out
}

// RemovePunct strips Unicode punctuation.
func RemovePunct(tokens []string) []string {
	var out []string
	for _, t := range tokens {
		clean := strings.Map(func(r rune) rune {
			if unicode.IsPunct(r) {
				return -1
			}
			return r
		}, t)
		if clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

// RemoveDiacritics decomposes and strips combining marks.
func RemoveDiacritics(s string) string {
	t := norm.NFD.String(s)
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, t)
}

// NormalizeTokens applies lowercase, diacritics removal, and punctuation stripping.
func NormalizeTokens(tokens []string) []string {
	toks := ToLowerASCII(tokens)
	var out []string
	for _, t := range toks {
		t = RemoveDiacritics(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return RemovePunct(out)
}
