package multiword

import "strings"

// Match multi-token gazetteer entries against a token stream.
func Match(tokens []string, gaz []string) []string {
	var out []string
	for _, ent := range gaz {
		parts := strings.Split(ent, " ")
		for i := 0; i <= len(tokens)-len(parts); i++ {
			match := true
			for j, p := range parts {
				if tokens[i+j] != p {
					match = false
					break
				}
			}
			if match {
				out = append(out, ent)
			}
		}
	}
	return out
}
