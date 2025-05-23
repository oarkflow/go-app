package grammar

import (
	"fmt"
	"regexp"
)

// Rule as loaded from grammar_rules.json
type Rule struct {
	Pattern    string `json:"pattern"`
	Suggestion string `json:"suggestion"`
}

type Grammar struct {
	SubjVerb   []Rule `json:"subject_verb_agreement"`
	Dangling   []Rule `json:"dangling_modifiers"`
	reSubjVerb []*regexp.Regexp
	reDangling []*regexp.Regexp
}

func New(rules *Grammar) (*Grammar, error) {
	if rules == nil {
		return nil, fmt.Errorf("grammar.New: received nil rules")
	}
	for _, rl := range rules.SubjVerb {
		re, err := regexp.Compile(rl.Pattern)
		if err != nil {
			return nil, err
		}
		rules.reSubjVerb = append(rules.reSubjVerb, re)
	}
	for _, rl := range rules.Dangling {
		re, err := regexp.Compile(rl.Pattern)
		if err != nil {
			return nil, err
		}
		rules.reDangling = append(rules.reDangling, re)
	}
	return rules, nil
}

// Check returns any suggestions found in text.
func (g *Grammar) Check(text string) []string {
	var sug []string
	for i, re := range g.reSubjVerb {
		if re.MatchString(text) {
			sug = append(sug, g.SubjVerb[i].Suggestion)
		}
	}
	for i, re := range g.reDangling {
		if re.MatchString(text) {
			sug = append(sug, g.Dangling[i].Suggestion)
		}
	}
	return sug
}
