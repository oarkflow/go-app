package main

import (
	"fmt"
	"strings"
)

// Document represents a single document in the search engine.
type Document struct {
	ID      string
	Content map[string]any // Supports flexible fields
}

// InvertedIndex stores the mapping from words to document IDs.
type InvertedIndex struct {
	index map[string]map[string]struct{} // word -> document IDs
}

// NewInvertedIndex initializes a new inverted index.
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{index: make(map[string]map[string]struct{})}
}

// AddDocument adds a document to the index.
func (ii *InvertedIndex) AddDocument(doc Document) {
	// Index the content of each field
	for _, value := range doc.Content {
		// Convert value to string and split into words
		valueStr := fmt.Sprintf("%v", value)
		words := strings.Fields(valueStr)

		for _, word := range words {
			word = strings.ToLower(word) // Normalize to lowercase
			if ii.index[word] == nil {
				ii.index[word] = make(map[string]struct{})
			}
			ii.index[word][doc.ID] = struct{}{}
		}
	}
}

// Search returns the document IDs containing the given term, with typo correction.
func (ii *InvertedIndex) Search(term string) (map[string]float64, error) {
	term = strings.ToLower(term)
	results := make(map[string]float64)

	// Check if the term exists
	if docIDs, exists := ii.index[term]; exists {
		for id := range docIDs {
			results[id] = 1.0 // Exact match score
		}
	} else {
		// If not found, suggest similar terms
		for word := range ii.index {
			distance := Levenshtein(term, word)
			if distance <= 2 { // Threshold for similarity
				for id := range ii.index[word] {
					score := 1.0 / (1.0 + float64(distance)) // Higher score for closer matches
					if _, exists := results[id]; !exists {
						results[id] = score
					} else {
						results[id] += score
					}
				}
			}
		}
	}

	return results, nil
}

// Levenshtein computes the Levenshtein distance between two strings.
func Levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	matrix := make([][]int, la+1)
	for i := range matrix {
		matrix[i] = make([]int, lb+1)
	}
	for i := range matrix {
		matrix[i][0] = i
	}
	for j := range matrix[0] {
		matrix[0][j] = j
	}

	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1, // Deletion
				min(
					matrix[i][j-1]+1,    // Insertion
					matrix[i-1][j]+cost, // Substitution
				),
			)
		}
	}
	return matrix[la][lb]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Example usage
func main() {
	index := NewInvertedIndex()

	// Sample documents
	documents := []Document{
		{"1", map[string]any{"title": "Golang Guide", "content": "Learn Go programming."}},
		{"2", map[string]any{"title": "Python Guide", "content": "Learn Python programming."}},
		{"3", map[string]any{"title": "Go in Practice", "content": "Advanced Go programming techniques."}},
		{"4", map[string]any{"title": "Data Science", "content": "Python is popular in data science."}},
	}

	// Add documents to the index
	for _, doc := range documents {
		index.AddDocument(doc)
	}

	// Search for a term with typo correction
	term := "golang" // Intentionally misspelled
	results, _ := index.Search(term)

	// Display results with scores
	fmt.Printf("Search results for '%s':\n", term)
	for id, score := range results {
		fmt.Printf("Document ID: %s, Score: %.2f\n", id, score)
	}
}
