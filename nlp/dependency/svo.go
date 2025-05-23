package dependency

import "strings"

// ExtractSVO finds the first noun-verb-noun triple.
func ExtractSVO(tokens, tags []string) (subj, verb, obj string) {
	for i := 0; i < len(tags)-2; i++ {
		if tags[i] == "NN" && strings.HasPrefix(tags[i+1], "VB") && tags[i+2] == "NN" {
			return tokens[i], tokens[i+1], tokens[i+2]
		}
	}
	return
}
