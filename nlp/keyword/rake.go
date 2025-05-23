package keyword

import (
	"regexp"
	"sort"
	"strings"
	
	"github.com/oarkflow/dag/nlp/stopwords"
)

func Extract(text string) []string {
	sep := regexp.MustCompile(`[,\.;:\?\!()\[\]"']+`)
	phrases := sep.Split(text, -1)
	
	freq := make(map[string]int)
	degree := make(map[string]int)
	wordRe := regexp.MustCompile(`\w+`)
	
	for _, ph := range phrases {
		words := wordRe.FindAllString(strings.ToLower(ph), -1)
		var cand []string
		for _, w := range words {
			if _, sw := stopwords.Set[w]; sw {
				if len(cand) > 0 {
					for _, cw := range cand {
						freq[cw]++
						degree[cw] += len(cand) - 1
					}
					cand = cand[:0]
				}
			} else {
				cand = append(cand, w)
			}
		}
		if len(cand) > 0 {
			for _, cw := range cand {
				freq[cw]++
				degree[cw] += len(cand) - 1
			}
		}
	}
	
	scores := make(map[string]float64)
	for w, f := range freq {
		scores[w] = float64(f+degree[w]) / float64(f)
	}
	
	type kv struct {
		Key string
		Val float64
	}
	var ss []kv
	for k, v := range scores {
		ss = append(ss, kv{k, v})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].Val > ss[j].Val })
	
	var out []string
	for _, kv := range ss {
		out = append(out, kv.Key)
	}
	return out
}
