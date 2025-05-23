package ngram

// ExtractNgrams returns frequency map of n-grams for a given n.
func ExtractNgrams(tokens []string, n int) map[string]int {
	counts := make(map[string]int)
	for i := 0; i <= len(tokens)-n; i++ {
		gram := tokens[i]
		for j := 1; j < n; j++ {
			gram += " " + tokens[i+j]
		}
		counts[gram]++
	}
	return counts
}
