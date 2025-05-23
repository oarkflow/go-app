package export

import "encoding/json"

type Annotation struct {
	Tokens []string          `json:"tokens"`
	Spans  map[string][2]int `json:"spans"`
	Dep    [][]string        `json:"dependencies"`
}

func ToJSON(a *Annotation) (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
