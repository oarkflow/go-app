package segmenter

import "regexp"

// SentenceSplit splits text into sentences (ends with . ! or ?).
var reSentence = regexp.MustCompile(`(?m)([^\.!?]+[\.!?])`)

func SentenceSplit(text string) []string {
	return reSentence.FindAllString(text, -1)
}

// ParagraphSplit splits text into paragraphs separated by ≥2 newlines.
var reParagraph = regexp.MustCompile(`\r?\n\s*\r?\n`)

func ParagraphSplit(text string) []string {
	return reParagraph.Split(text, -1)
}
