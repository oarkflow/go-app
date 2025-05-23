package sentiment

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

var Lexicon map[string]int

func init() {
	Lexicon = make(map[string]int)
	f, err := os.Open("data/afinn.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		parts := strings.Split(scan.Text(), "\t")
		if len(parts) == 2 {
			if v, err := strconv.Atoi(parts[1]); err == nil {
				Lexicon[parts[0]] = v
			}
		}
	}
}

// SentimentScore sums token sentiment weights.
func SentimentScore(tokens []string) int {
	sum := 0
	for _, t := range tokens {
		if v, ok := Lexicon[t]; ok {
			sum += v
		}
	}
	return sum
}

// extend existing SentimentScore to flip within negation scope
func SentimentScoreNeg(tokens []string) int {
	score, negated := 0, false
	for _, t := range tokens {
		if t == "not" || t == "never" {
			negated = true
			continue
		}
		v := Lexicon[t]
		if negated {
			v = -v
			negated = false
		}
		score += v
	}
	return score
}
