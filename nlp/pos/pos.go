package pos

import "regexp"

var (
	reVBG = regexp.MustCompile(`ing$`)
	reVBD = regexp.MustCompile(`ed$`)
	reRB  = regexp.MustCompile(`ly$`)
	reVB  = regexp.MustCompile(`^is$|^are$|^be$`)
)

// Tag assigns a coarse POS tag to each token.
func Tag(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		switch {
		case reVBG.MatchString(t):
			out[i] = "VBG"
		case reVBD.MatchString(t):
			out[i] = "VBD"
		case reRB.MatchString(t):
			out[i] = "RB"
		case reVB.MatchString(t):
			out[i] = "VB"
		default:
			out[i] = "NN"
		}
	}
	return out
}
