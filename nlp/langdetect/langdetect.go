package langdetect

import (
	"bufio"
	"os"
)

var stopEn, stopEs map[string]struct{}

func init() {
	stopEn = load("data/stopwords.txt")
	stopEs = load("data/stopwords_es.txt")
}

func load(path string) map[string]struct{} {
	m := make(map[string]struct{})
	f, _ := os.Open(path)
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		m[scan.Text()] = struct{}{}
	}
	return m
}

// Detect returns "en" or "es" by stopword overlap.
func Detect(tokens []string) string {
	en, es := 0, 0
	for _, w := range tokens {
		if _, ok := stopEn[w]; ok {
			en++
		}
		if _, ok := stopEs[w]; ok {
			es++
		}
	}
	if es > en {
		return "es"
	}
	return "en"
}
