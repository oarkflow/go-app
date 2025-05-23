package segmentation

type Config struct {
	GermanMinLen int `json:"german_compound_min_length"`
}

func SplitGerman(word string, cfg *Config) []string {
	// naive: split into all substrings ≥ minLen
	var out []string
	n := len(word)
	for l := cfg.GermanMinLen; l < n; l++ {
		for i := 0; i+l <= n; i++ {
			out = append(out, word[i:i+l])
		}
	}
	return out
}
