package event

import (
	"regexp"
)

type Pattern struct {
	Regex string   `json:"regex"`
	Slots []string `json:"slots"`
}

type Config struct {
	Patterns []Pattern `json:"patterns"`
	re       []*regexp.Regexp
}

func New(cfg *Config) (*Config, error) {
	for _, p := range cfg.Patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, err
		}
		cfg.re = append(cfg.re, re)
	}
	return cfg, nil
}

// Extract returns list of slot maps per match.
func (c *Config) Extract(text string) []map[string]string {
	var out []map[string]string
	for i, re := range c.re {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			slotMap := make(map[string]string)
			for j, name := range c.Patterns[i].Slots {
				slotMap[name] = m[j+1]
			}
			out = append(out, slotMap)
		}
	}
	return out
}
