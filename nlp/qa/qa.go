package qa

import (
	"fmt"
	"regexp"
)

type QAPatterns struct {
	Who  string `json:"who"`
	When string `json:"when"`
}

func Answer(patterns *QAPatterns, question string) string {
	if re := regexp.MustCompile(patterns.Who); re.MatchString(question) {
		// simple: return the captured group
		return fmt.Sprintf("Answer: %s", re.ReplaceAllString(question, "$1"))
	}
	if re := regexp.MustCompile(patterns.When); re.MatchString(question) {
		return fmt.Sprintf("Answer: %s", re.ReplaceAllString(question, "$1"))
	}
	return "I don't know."
}
