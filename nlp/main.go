package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	
	"github.com/oarkflow/dag/nlp/config"
	"github.com/oarkflow/dag/nlp/dependency"
	"github.com/oarkflow/dag/nlp/discourse"
	"github.com/oarkflow/dag/nlp/event"
	"github.com/oarkflow/dag/nlp/export"
	"github.com/oarkflow/dag/nlp/grammar"
	"github.com/oarkflow/dag/nlp/keyword"
	"github.com/oarkflow/dag/nlp/langdetect"
	"github.com/oarkflow/dag/nlp/measurement"
	"github.com/oarkflow/dag/nlp/morphology"
	"github.com/oarkflow/dag/nlp/multiword"
	"github.com/oarkflow/dag/nlp/ner"
	"github.com/oarkflow/dag/nlp/ngram"
	"github.com/oarkflow/dag/nlp/normalizer"
	"github.com/oarkflow/dag/nlp/pos"
	"github.com/oarkflow/dag/nlp/qa"
	"github.com/oarkflow/dag/nlp/segmentation"
	"github.com/oarkflow/dag/nlp/segmenter"
	"github.com/oarkflow/dag/nlp/sentiment"
	"github.com/oarkflow/dag/nlp/spellcheck"
	"github.com/oarkflow/dag/nlp/stemmer"
	"github.com/oarkflow/dag/nlp/stopwords"
	"github.com/oarkflow/dag/nlp/streaming"
	"github.com/oarkflow/dag/nlp/summarization"
	"github.com/oarkflow/dag/nlp/tfidf"
	"github.com/oarkflow/dag/nlp/tokenizer"
	"github.com/oarkflow/dag/nlp/topic"
	"github.com/oarkflow/dag/nlp/translit"
	"github.com/oarkflow/dag/nlp/wsd"
)

func mustLoadJSON[T any](dataDir, fname string) *T {
	path := filepath.Join(dataDir, fname)
	cfg, err := config.LoadJSON[T](path)
	if err != nil {
		log.Fatalf("failed to load %s: %v", fname, err)
	}
	if cfg == nil {
		log.Fatalf("%s was empty", fname)
	}
	return cfg
}

func main() {
	dataDir := flag.String("data", "data", "data directory containing all JSON/TXT/CSV files")
	flag.Parse()
	
	// 1) Grammar
	{
		grules := mustLoadJSON[grammar.Grammar](*dataDir, "grammar_rules.json")
		checker, err := grammar.New(grules)
		if err != nil {
			log.Fatalf("grammar compile: %v", err)
		}
		sample := "Is John like apples, walking quickly."
		fmt.Println("Grammar suggestions:", checker.Check(sample))
	}
	
	// 2) Summarization
	{
		summCfg := mustLoadJSON[summarization.Summarizer](*dataDir, "summarization_config.json")
		docs := [][]string{
			{"John", "went", "to", "London"},
			{"He", "loved", "the", "trip"},
			{"There", "were", "typos"},
			{"He", "visited", "Paris"},
		}
		fmt.Println("Summary:", summCfg.Summarize(docs, docs))
	}
	
	// 3) QA
	{
		qaCfg := mustLoadJSON[qa.QAPatterns](*dataDir, "qa_patterns.json")
		fmt.Println(qa.Answer(qaCfg, "Who is Alice?"))
		fmt.Println(qa.Answer(qaCfg, "When did WW2 happen?"))
	}
	
	// 4) Event Extraction
	{
		evCfg := mustLoadJSON[event.Config](*dataDir, "event_patterns.json")
		evCfg, err := event.New(evCfg)
		if err != nil {
			log.Fatalf("event compile: %v", err)
		}
		fmt.Println("Events:", evCfg.Extract("John went to Paris on 2024-06-01"))
	}
	
	// 5) Measurement
	{
		muCfg := mustLoadJSON[measurement.Units](*dataDir, "measurement_units.json")
		fmt.Println("Measurements:", measurement.Normalize("Paid $1,234.56 and 12 kg and 3/4 loaf", muCfg))
	}
	
	// 6) Transliteration
	{
		maps := mustLoadJSON[translit.Maps](*dataDir, "transliteration_map.json")
		fmt.Println("To IPA:", translit.Transliterate("abc", maps, "ipa"))
		fmt.Println("Cyr->Lat:", translit.Transliterate("АБВ", maps, "c2l"))
	}
	
	// 7) German compound splitting
	{
		segCfg := mustLoadJSON[segmentation.Config](*dataDir, "segmentation_rules.json")
		comps := segmentation.SplitGerman("Donaudampfschifffahrtsgesellschaft", segCfg)
		fmt.Println("First 3 compounds:", comps[:3])
	}
	
	// 8) Streaming tokens
	fmt.Print("Streaming: ")
	streaming.ProcessTokens(strings.NewReader("hello world"), func(tok string) {
		fmt.Print(tok, "|")
	})
	fmt.Println()
	
	// 9) Core pipeline
	text := "Alice Smith visited Berlin."
	toks := tokenizer.TokenizeSubwords(text)
	norm := normalizer.NormalizeTokens(toks)
	filt := stopwords.Filter(norm)
	stems := make([]string, len(filt))
	for i, w := range filt {
		stems[i] = stemmer.PorterStem(w)
	}
	tags := pos.Tag(filt)
	ents := ner.Recognize(toks)
	fmt.Println("Pipeline:", filt, stems, tags, ents)
	
	// 10) Spellcheck
	fmt.Println("Spellcheck:", spellcheck.Suggest("spelcheck"))
	
	// 11) Keywords
	fmt.Println("Keywords:", keyword.Extract("Quick brown fox jumps over lazy dog."))
	
	// 12) Morphology
	stem, aff := morphology.MorphAnalyze("unhappiness")
	fmt.Println("Morphology:", stem, aff)
	
	// 13) Sentiment & Negation
	fmt.Println("Sentiment:", sentiment.SentimentScore(norm))
	fmt.Println("Negation Sentiment:", sentiment.SentimentScoreNeg(append(norm, "not", "good")))
	
	// 14) Dependency SVO
	subj, v, obj := dependency.ExtractSVO(filt, tags)
	fmt.Println("SVO:", subj, v, obj)
	
	// 15) TF-IDF & Ranking
	docs := [][]string{filt, {"alice", "visited", "berlin"}}
	corp := tfidf.NewCorpus(docs)
	fmt.Println("Rankings:", corp.Rank(filt))
	
	// 16) N-grams
	fmt.Println("Bigrams:", ngram.ExtractNgrams(filt, 2))
	
	// 17) Language Detection
	fmt.Println("Lang EN:", langdetect.Detect(filt))
	fmt.Println("Lang ES:", langdetect.Detect([]string{"hola", "mundo"}))
	
	// 18) WSD
	fmt.Println("Bank sense:", wsd.Disambiguate("bank"))
	
	// 19) Multiword Entities
	fmt.Println("Multiword:", multiword.Match(toks, []string{"Berlin", "Alice Smith"}))
	
	// 20) Topic Clustering
	centers := topic.Cluster(corp.TFIDFCache, 2, 5)
	fmt.Println("Topic centers:", centers)
	
	// 21) Discourse Coherence
	sents := segmenter.SentenceSplit(text)
	tokSents := make([][]string, len(sents))
	entSents := make([][]string, len(sents))
	for i, s := range sents {
		t := tokenizer.TokenizeSubwords(s)
		tokSents[i] = normalizer.NormalizeTokens(t)
		entSents[i] = ner.Recognize(t)
	}
	fmt.Println("Coherence:", discourse.Coherence(tokSents, entSents))
	
	// 22) Export
	ann := export.Annotation{
		Tokens: toks,
		Spans:  map[string][2]int{"example": {0, len(toks) - 1}},
		Dep:    [][]string{{"nsubj", "visited", "Alice"}},
	}
	jsonAnn, _ := export.ToJSON(&ann)
	fmt.Println("Export JSON:", jsonAnn)
}
