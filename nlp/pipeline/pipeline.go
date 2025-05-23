package pipeline

import (
	"fmt"
	
	"github.com/oarkflow/dag/nlp/normalizer"
	"github.com/oarkflow/dag/nlp/pos"
	"github.com/oarkflow/dag/nlp/segmenter"
	"github.com/oarkflow/dag/nlp/tokenizer"
)

// Pipeline demonstrates concurrent processing: paragraph → sentence → tokenize → normalize → POS.
func Run(text string) {
	paras := segmenter.ParagraphSplit(text)
	sentC := make(chan string)
	tokC := make(chan []string)
	
	// Paragraph → Sentence
	go func() {
		for _, p := range paras {
			for _, s := range segmenter.SentenceSplit(p) {
				sentC <- s
			}
		}
		close(sentC)
	}()
	
	// Sentence → Tokens → Normalized Tokens
	go func() {
		for s := range sentC {
			tks := tokenizer.TokenizeSubwords(s)
			tokC <- normalizer.NormalizeTokens(tks)
		}
		close(tokC)
	}()
	
	// Consume: POS tag each normalized token list
	for normed := range tokC {
		tags := pos.Tag(normed)
		fmt.Println("→", normed, tags)
	}
}
