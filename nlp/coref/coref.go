package coref

// Link pronouns (“he”, “she”, “it”) to the nearest preceding entity.
func Resolve(tokens []string, entities []string) map[int]string {
	res := make(map[int]string)
	lastEntIdx := -1
	for i, t := range tokens {
		for _, e := range entities {
			if t == e {
				lastEntIdx = i
			}
		}
		if t == "he" || t == "she" || t == "it" {
			if lastEntIdx >= 0 {
				res[i] = tokens[lastEntIdx]
			}
		}
	}
	return res
}
