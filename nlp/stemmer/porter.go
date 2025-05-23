package stemmer

// PorterStem is a minimal Porter-style stemmer.
func PorterStem(word string) string {
	if len(word) > 4 && word[len(word)-2:] == "es" {
		return word[:len(word)-2]
	}
	if len(word) > 3 && word[len(word)-1:] == "s" {
		return word[:len(word)-1]
	}
	return word
}
