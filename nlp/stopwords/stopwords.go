package stopwords

import (
	"bufio"
	"os"
	"strings"
)

var Set map[string]struct{}

func init() {
	Set = make(map[string]struct{})
	f, err := os.Open("data/stopwords.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		w := strings.TrimSpace(scan.Text())
		if w != "" {
			Set[w] = struct{}{}
		}
	}
}

// Filter removes any token present in the stopword set.
func Filter(tokens []string) []string {
	var out []string
	for _, t := range tokens {
		if _, isStop := Set[t]; !isStop {
			out = append(out, t)
		}
	}
	return out
}
