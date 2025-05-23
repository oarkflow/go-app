package translit

type Maps struct {
	LatinToIPA      map[string]string `json:"LatinToIPA"`
	CyrillicToLatin map[string]string `json:"CyrillicToLatin"`
}

func Transliterate(input string, m *Maps, mode string) string {
	out := ""
	for _, r := range input {
		ch := string(r)
		switch mode {
		case "ipa":
			if x, ok := m.LatinToIPA[ch]; ok {
				out += x
			} else {
				out += ch
			}
		case "c2l":
			if x, ok := m.CyrillicToLatin[ch]; ok {
				out += x
			} else {
				out += ch
			}
		default:
			out += ch
		}
	}
	return out
}
