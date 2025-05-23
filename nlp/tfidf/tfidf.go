package tfidf

import (
	"math"
	"sort"
)

// Document-term matrix builder & TF-IDF scorer.
type Corpus struct {
	Docs       [][]string
	DF         map[string]int
	IDF        map[string]float64
	TFIDFCache []map[string]float64
}

func NewCorpus(docs [][]string) *Corpus {
	c := &Corpus{Docs: docs, DF: make(map[string]int)}
	// Document frequencies
	for _, doc := range docs {
		seen := make(map[string]bool)
		for _, w := range doc {
			if !seen[w] {
				c.DF[w]++
				seen[w] = true
			}
		}
	}
	// IDF
	N := float64(len(docs))
	c.IDF = make(map[string]float64)
	for w, df := range c.DF {
		c.IDF[w] = math.Log(N/float64(df)) + 1.0
	}
	// Precompute TF-IDF per doc
	c.TFIDFCache = make([]map[string]float64, len(docs))
	for i, doc := range docs {
		tf := make(map[string]int)
		for _, w := range doc {
			tf[w]++
		}
		m := make(map[string]float64)
		for w, cnt := range tf {
			m[w] = float64(cnt) / float64(len(doc)) * c.IDF[w]
		}
		c.TFIDFCache[i] = m
	}
	return c
}

// Rank returns doc indices sorted by cosine similarity to query.
func (c *Corpus) Rank(query []string) []int {
	// compute TF for query
	qtf := make(map[string]int)
	for _, w := range query {
		qtf[w]++
	}
	qvec := make(map[string]float64)
	for w, cnt := range qtf {
		if idf, ok := c.IDF[w]; ok {
			qvec[w] = float64(cnt) / float64(len(query)) * idf
		}
	}
	// cosine similarity
	type kv struct {
		idx int
		sim float64
	}
	var sims []kv
	for i, docvec := range c.TFIDFCache {
		num, denA, denB := 0.0, 0.0, 0.0
		for w, qw := range qvec {
			num += qw * docvec[w]
		}
		for _, qw := range qvec {
			denA += qw * qw
		}
		for _, dw := range docvec {
			denB += dw * dw
		}
		if denA == 0 || denB == 0 {
			sims = append(sims, kv{i, 0})
		} else {
			sims = append(sims, kv{i, num / (math.Sqrt(denA) * math.Sqrt(denB))})
		}
	}
	sort.Slice(sims, func(i, j int) bool { return sims[i].sim > sims[j].sim })
	idxs := make([]int, len(sims))
	for i, kv := range sims {
		idxs[i] = kv.idx
	}
	return idxs
}
