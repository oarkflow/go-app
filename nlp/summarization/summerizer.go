package summarization

import (
	"sort"
	
	"github.com/oarkflow/dag/nlp/tfidf"
)

type Summarizer struct {
	MaxSentences int `json:"max_sentences"`
}

// Extractive summary: score by tfidf+coherence, pick top N sentences.
func (s *Summarizer) Summarize(sentences [][]string, docs [][]string) [][]string {
	// build TF-IDF corpus
	corp := tfidf.NewCorpus(docs)
	scores := make([]struct {
		idx   int
		score float64
	}, len(sentences))
	for i, sent := range sentences {
		// score = average tfidf + adjacency coherence
		ranks := corp.Rank(sent)
		score := float64(len(ranks) - indexOf(ranks, -1)) // rough proxy
		scores[i] = struct {
			idx   int
			score float64
		}{i, score}
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})
	var summary [][]string
	for i := 0; i < s.MaxSentences && i < len(scores); i++ {
		summary = append(summary, sentences[scores[i].idx])
	}
	return summary
}

func indexOf(slice []int, v int) int {
	for i, x := range slice {
		if x == v {
			return i
		}
	}
	return len(slice)
}
