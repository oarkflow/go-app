package lemmatizer

import (
	"bufio"
	"os"
	"strings"
)

var Dict map[string]string

func init() {
	Dict = make(map[string]string)
	f, err := os.Open("data/lemma_dict.csv")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		parts := strings.Split(scan.Text(), ",")
		if len(parts) == 2 {
			Dict[parts[0]] = parts[1]
		}
	}
}

// Lemma returns the base form if present.
func Lemma(token string) string {
	if l, ok := Dict[token]; ok {
		return l
	}
	return token
}
