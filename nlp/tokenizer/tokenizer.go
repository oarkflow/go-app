package tokenizer

import "regexp"

// TokenizeSubwords splits text into words or numbers, preserving internal apostrophes.
var reToken = regexp.MustCompile(`[’']?[\pL]+[’']?|\pN+`)

func TokenizeSubwords(text string) []string {
	return reToken.FindAllString(text, -1)
}
