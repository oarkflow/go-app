package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	tree := `nlp/
├── go.mod
├── main.go
├── data/
│   ├── stopwords.txt
│   ├── lemma_dict.csv
│   ├── gazetteer.txt
│   ├── dict.txt
│   ├── afinn.txt
│   ├── afinn_negation.txt
│   ├── stopwords_es.txt
│   └── wordnet_senses.csv
├── tokenizer/…
├── segmenter/…
├── normalizer/…
├── stopwords/…
├── stemmer/…
├── lemmatizer/…
├── pos/…
├── ner/…
├── spellcheck/…
├── keyword/…
├── morphology/…
├── sentiment/…
├── dependency/…
├── pipeline/…
├── tfidf/
│   └── tfidf.go
├── ngram/
│   └── ngram.go
├── langdetect/
│   └── langdetect.go
├── datetime/
│   └── normalize.go
├── coref/
│   └── coref.go
├── wsd/
│   └── wsd.go
├── multiword/
│   └── matcher.go
├── topic/
│   └── kmeans.go
└── discourse/
    └── coherence.go`
	
	err := CreateStructureFromText(".", tree)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("Structure created successfully.")
	}
}

// CreateStructureFromText parses a tree structure from a string and creates files/folders.
func CreateStructureFromText(basePath string, treeText string) error {
	scanner := bufio.NewScanner(strings.NewReader(treeText))
	
	type DirLevel struct {
		Indent int
		Path   string
		Ignore bool
	}
	
	var stack []DirLevel
	
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		
		// Count indentation
		indent := strings.IndexFunc(line, func(r rune) bool {
			return r != ' ' && r != '│' && r != '├' && r != '─' && r != '└'
		})
		
		name := strings.TrimSpace(line[indent:])
		name = strings.TrimPrefix(name, "└── ")
		name = strings.TrimPrefix(name, "├── ")
		
		// Determine parent path and ignore status
		for len(stack) > 0 && stack[len(stack)-1].Indent >= indent {
			stack = stack[:len(stack)-1]
		}
		
		var fullPath string
		ignore := false
		if len(stack) == 0 {
			fullPath = filepath.Join(basePath, name)
		} else {
			parent := stack[len(stack)-1]
			fullPath = filepath.Join(parent.Path, name)
			ignore = parent.Ignore
		}
		
		// Skip if the current path should be ignored
		if ignore {
			continue
		}
		
		// Skip folders like "segmenter/…"
		if strings.HasSuffix(name, "/…") {
			dir := strings.TrimSuffix(name, "/…")
			fullPath = filepath.Join(basePath, dir)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				err := os.MkdirAll(fullPath, os.ModePerm)
				if err != nil {
					return fmt.Errorf("failed to create directory %s: %w", fullPath, err)
				}
			}
			stack = append(stack, DirLevel{Indent: indent, Path: fullPath, Ignore: true})
			continue
		}
		
		if strings.HasSuffix(name, "/") {
			// It's a directory
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				err := os.MkdirAll(fullPath, os.ModePerm)
				if err != nil {
					return fmt.Errorf("failed to create directory %s: %w", fullPath, err)
				}
			}
			stack = append(stack, DirLevel{Indent: indent, Path: fullPath, Ignore: false})
		} else {
			// It's a file
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				dir := filepath.Dir(fullPath)
				err := os.MkdirAll(dir, os.ModePerm)
				if err != nil {
					return fmt.Errorf("failed to create directory for file %s: %w", fullPath, err)
				}
				file, err := os.Create(fullPath)
				if err != nil {
					return fmt.Errorf("failed to create file %s: %w", fullPath, err)
				}
				file.Close()
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		return err
	}
	
	return nil
}
