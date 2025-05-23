package measurement

import (
	"fmt"
	"regexp"
	"strconv"
)

type Units struct {
	Units    map[string]string `json:"units"`
	Currency map[string]string `json:"currency"`
}

var (
	reMoney = regexp.MustCompile(`(\p{Sc})([0-9,\.]+)`)
	reNum   = regexp.MustCompile(`(\d+(?:/\d+)?|\d+(?:\.\d+)?)(?:\s*)([A-Za-z]+)`)
)

func Normalize(input string, cfg *Units) []string {
	var out []string
	// currency
	for _, m := range reMoney.FindAllStringSubmatch(input, -1) {
		sym, amt := m[1], m[2]
		code := cfg.Currency[sym]
		out = append(out, fmt.Sprintf(`{"currency":"%s","amount":%s}`, code, amt))
	}
	// units & fractions
	for _, m := range reNum.FindAllStringSubmatch(input, -1) {
		val, unit := m[1], m[2]
		if _, ok := cfg.Units[unit]; ok {
			if frac := regexp.MustCompile(`/`).MatchString(val); frac {
				parts := regexp.MustCompile(`/`).Split(val, 2)
				num, _ := strconv.Atoi(parts[0])
				den, _ := strconv.Atoi(parts[1])
				out = append(out, fmt.Sprintf(`{"unit":"%s","value":%f}`, unit, float64(num)/float64(den)))
			} else {
				out = append(out, fmt.Sprintf(`{"unit":"%s","value":%s}`, unit, val))
			}
		}
	}
	return out
}
