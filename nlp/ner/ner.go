package ner

import (
	"bufio"
	"os"
	"strings"
)

var Gazetteer map[string]struct{}

func init() {
	Gazetteer = make(map[string]struct{})
	f, err := os.Open("data/gazetteer.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		name := strings.TrimSpace(scan.Text())
		if name != "" {
			Gazetteer[name] = struct{}{}
		}
	}
}

// Recognize returns tokens that match the gazetteer.
func Recognize(tokens []string) []string {
	var ents []string
	for _, t := range tokens {
		if _, ok := Gazetteer[t]; ok {
			ents = append(ents, t)
		}
	}
	return ents
}
