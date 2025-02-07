package bcl

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

func WordWithArticle(s string) string {
	c, _ := utf8.DecodeRune([]byte(s))

	var article string

	switch c {
	case 'a', 'e', 'i', 'o', 'u':
		article = "an"
	default:
		article = "a"
	}

	return article + " " + s
}

func WordsEnumerationAnd(ss []string) string {
	return wordsEnumeration(ss, " and ")
}

func WordsEnumerationOr(ss []string) string {
	return wordsEnumeration(ss, " or ")
}

func wordsEnumeration(ss []string, lastSeparator string) (se string) {
	switch len(ss) {
	case 1:
		se = ss[0]

	case 2:
		se = ss[0] + lastSeparator + ss[1]

	default:
		var buf bytes.Buffer

		for i := 0; i < len(ss)-1; i++ {
			if i > 0 {
				buf.WriteString(", ")
			}

			buf.WriteString(ss[i])
		}

		buf.WriteString(lastSeparator)
		buf.WriteString(ss[len(ss)-1])

		se = buf.String()
	}

	return
}

func PluralizeWord(s string, n int) (ps string) {
	// Obviously wrong in so many cases. To be updated on a case-by-case basis.

	if n == 1 {
		ps = s
		return
	}

	suffix := func(suffix string) bool {
		return strings.HasSuffix(s, suffix)
	}

	switch {
	case suffix("s"), suffix("ss"), suffix("sh"), suffix("ch"):
		ps = s + "es"
	case suffix("x"), suffix("z"), suffix("o"):
		ps = s + "es"
	case suffix("is"):
		ps = s[:len(s)-2] + "es"
	case suffix("on"):
		ps = s[:len(s)-2] + "a"
	default:
		ps = s + "s"
	}

	return
}
