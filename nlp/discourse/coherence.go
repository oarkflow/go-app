package discourse

// Simple coherence: count shared entities between adjacent sentences.
func Coherence(sentences [][]string, entities [][]string) float64 {
	totalPairs := 0
	sharedCount := 0
	for i := 0; i < len(sentences)-1; i++ {
		totalPairs++
		e1 := make(map[string]struct{})
		for _, e := range entities[i] {
			e1[e] = struct{}{}
		}
		for _, e := range entities[i+1] {
			if _, ok := e1[e]; ok {
				sharedCount++
				break
			}
		}
	}
	if totalPairs == 0 {
		return 0
	}
	return float64(sharedCount) / float64(totalPairs)
}
