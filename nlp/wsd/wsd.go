package wsd

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

var senses map[string]map[string]float64

func init() {
	// CSV: word,sense,score
	senses = make(map[string]map[string]float64)
	f, _ := os.Open("data/wordnet_senses.csv")
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		parts := strings.Split(scan.Text(), ",")
		w, sense, sc := parts[0], parts[1], atof(parts[2])
		if senses[w] == nil {
			senses[w] = make(map[string]float64)
		}
		senses[w][sense] = sc
	}
}

// Disambiguate picks the sense with highest score.
func Disambiguate(word string) string {
	best, bestScore := "", 0.0
	for sense, sc := range senses[word] {
		if sc > bestScore {
			bestScore, best = sc, sense
		}
	}
	if best == "" {
		return word
	}
	return best
}

func atof(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
