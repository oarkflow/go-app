package morphology

import "strings"

// Affix lists
var prefixes = []string{"un", "re", "in", "im", "dis", "pre"}
var suffixes = []string{"ness", "ment", "able", "ible", "tion", "sion"}

// MorphAnalyze splits word into stem and affixes.
func MorphAnalyze(word string) (stem string, affixes []string) {
	stem = word
	for _, suf := range suffixes {
		if strings.HasSuffix(stem, suf) && len(stem) > len(suf)+2 {
			affixes = append(affixes, suf)
			stem = strings.TrimSuffix(stem, suf)
		}
	}
	for _, pre := range prefixes {
		if strings.HasPrefix(stem, pre) && len(stem) > len(pre)+2 {
			affixes = append(affixes, pre)
			stem = strings.TrimPrefix(stem, pre)
		}
	}
	return
}
