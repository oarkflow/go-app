package bcl

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

type TokenType string

const (
	TokenTypeEOL            TokenType = "eol"
	TokenTypeOpeningBracket TokenType = "opening_bracket"
	TokenTypeClosingBracket TokenType = "closing_bracket"
	TokenTypeSymbol         TokenType = "symbol"
	TokenTypeString         TokenType = "string"
	TokenTypeInteger        TokenType = "integer"
	TokenTypeFloat          TokenType = "float"
)

type Token struct {
	Type  TokenType
	Data  string
	Value any
	Span  Span
}

func (t *Token) String() string {
	if t.Value == nil {
		return fmt.Sprintf("Token{%s}", t.Type)
	}

	return fmt.Sprintf("Token{%s, %v}", t.Type, t.Value)
}

type tokenizer struct {
	source string
	data   []byte
	point  Point
}

func newTokenizer(data []byte, source string) *tokenizer {
	return &tokenizer{
		source: source,
		data:   data,
		point:  Point{Offset: 0, Line: 1, Column: 1},
	}
}

func (t *tokenizer) syntaxError(format string, args ...any) error {
	return t.syntaxErrorAtPoint(t.point, format, args...)
}

func (t *tokenizer) syntaxErrorAtPoint(point Point, format string, args ...any) error {
	return t.syntaxErrorAt(Span{point, point}, format, args...)
}

func (t *tokenizer) syntaxErrorAt(span Span, format string, args ...any) error {
	return &SyntaxError{
		Source:      t.source,
		Location:    span,
		Description: fmt.Sprintf(format, args...),
	}
}

func (t *tokenizer) readToken() *Token {
	for {
		t.skipWhitespaces()
		if len(t.data) == 0 {
			return nil
		}

		c := t.peekChar()

		if c == '#' {
			t.skipComment()
			continue
		}

		if c == '\\' {
			t.skip(1)
			if t.skipEOL() > 0 {
				continue
			}

			panic(t.syntaxError("missing EOL sequence after " +
				"line continuation character"))
		}

		data := t.data
		start := t.point

		if eolLen := t.skipEOL(); eolLen > 0 {
			span := NewSpanAt(start, eolLen)
			return &Token{
				Type: TokenTypeEOL,
				Span: span,
				Data: string(data[:span.Len()]),
			}
		}

		switch {
		case c == '{':
			t.skip(1)

			return &Token{
				Type: TokenTypeOpeningBracket,
				Span: NewSpanAt(start, 1),
				Data: "\n",
			}

		case c == '}':
			t.skip(1)

			return &Token{
				Type: TokenTypeClosingBracket,
				Span: NewSpanAt(start, 1),
				Data: "\n",
			}

		case c >= 'a' && c <= 'z':
			return t.readSymbolToken()

		case c == '+' || c == '-' || (c >= '0' && c <= '9'):
			return t.readNumberToken()

		case c == '"':
			return t.readStringToken()
		}

		panic(t.syntaxError("unexpected character %q", c))
	}
}

func (t *tokenizer) readSymbolToken() *Token {
	data := t.data
	start := t.point

	var symbol []rune

	for len(t.data) > 0 {
		c := t.peekChar()

		if isWhitespaceOrEOLChar(c) {
			break
		}

		if len(symbol) == 0 {
			if !isSymbolFirstChar(c) {
				panic(t.syntaxError("invalid symbol first character %q", c))
			}
		} else {
			if !isSymbolChar(c) {
				panic(t.syntaxError("invalid symbol character %q", c))
			}
		}

		symbol = append(symbol, c)

		t.skip(1)
	}

	span := NewSpanAt(start, len(symbol))

	return &Token{
		Type:  TokenTypeSymbol,
		Span:  span,
		Data:  string(data[:span.Len()]),
		Value: string(symbol),
	}
}

func (t *tokenizer) readNumberToken() *Token {
	data := t.data
	start := t.point

	var s []rune
	var isFloat bool
	var hasExponent bool

	// Sign
	if c := t.peekChar(); c == '+' || c == '-' {
		s = append(s, c)
		t.skip(1)
	}

	// Integer part
	if len(t.data) == 0 {
		panic(t.syntaxError("truncated number"))
	}

	if c := t.peekChar(); c < '0' || c > '9' {
		panic(t.syntaxError("invalid number character %q", c))
	}

	for len(t.data) > 0 {
		c := t.peekChar()

		if c >= '0' && c <= '9' {
			s = append(s, c)
			t.skip(1)
		} else if c == '.' || c == 'e' || c == 'E' {
			isFloat = true
			s = append(s, c)
			t.skip(1)
			break
		} else if isWordBoundary(c) {
			break
		} else {
			panic(t.syntaxError("invalid number character %q", c))
		}
	}

	if !isFloat {
		i, err := strconv.ParseInt(string(s), 10, 64)
		if err != nil {
			panic(t.syntaxErrorAtPoint(start, "invalid integer: %v", err))
		}

		span := NewSpanAt(start, len(s))

		return &Token{
			Type:  TokenTypeInteger,
			Span:  span,
			Data:  string(data[:span.Len()]),
			Value: i,
		}
	}

	// Fractional part
	if len(t.data) == 0 {
		panic(t.syntaxError("truncated number"))
	}

	if c := t.peekChar(); c < '0' || c > '9' {
		panic(t.syntaxError("invalid number character %q", c))
	}

	for len(t.data) > 0 {
		c := t.peekChar()

		if c >= '0' && c <= '9' {
			s = append(s, c)
			t.skip(1)
		} else if c == 'e' || c == 'E' {
			hasExponent = true
			s = append(s, c)
			t.skip(1)
			break
		} else if isWordBoundary(c) {
			break
		} else {
			panic(t.syntaxError("invalid number character %q", c))
		}
	}

	// Exponent
	if hasExponent {
		if len(t.data) == 0 {
			panic(t.syntaxError("truncated number"))
		}

		if c := t.peekChar(); c == '+' || c == '-' {
			s = append(s, c)
			t.skip(1)

			if len(t.data) == 0 {
				panic(t.syntaxError("truncated number"))
			}
		} else if c < '0' || c > '9' {
			panic(t.syntaxError("invalid exponent character %q", c))
		}

		for len(t.data) > 0 {
			c := t.peekChar()

			if c >= '0' && c <= '9' {
				s = append(s, c)
				t.skip(1)
			} else if isWordBoundary(c) {
				break
			} else {
				panic(t.syntaxError("invalid number character %q", c))
			}
		}
	}

	f, err := strconv.ParseFloat(string(s), 64)
	if err != nil {
		panic(t.syntaxErrorAtPoint(start, "invalid float: %v", err))
	}

	span := NewSpanAt(start, len(s))

	return &Token{
		Type:  TokenTypeFloat,
		Span:  span,
		Data:  string(data[:span.Len()]),
		Value: f,
	}
}

func (t *tokenizer) readStringToken() *Token {
	data := t.data
	start := t.point

	var s []rune

	t.skip(1) // '"'

	charLen := 1
	byteLen := 1

	for len(t.data) > 0 {
		if len(t.data) == 0 {
			panic(t.syntaxError("truncated string"))
		}

		point := t.point
		c := t.peekChar()

		if c < 0x20 {
			panic(t.syntaxErrorAtPoint(point, "invalid string character %q", c))
		}

		if c == '"' {
			charLen++
			byteLen++

			t.skip(1)
			break
		}

		if c == '\\' {
			charLen++
			byteLen++

			t.skip(1)
			if len(t.data) == 0 {
				panic(t.syntaxErrorAtPoint(point, "truncated escape sequence"))
			}

			c = t.peekChar()

			switch c {
			case 'a':
				c = '\a'
			case 'b':
				c = '\b'
			case 't':
				c = '\t'
			case 'n':
				c = '\n'
			case 'v':
				c = '\v'
			case 'f':
				c = '\f'
			case 'r':
				c = '\r'
			case '"':
			case '\\':

			default:
				span := NewSpanAt(point, 2)
				panic(t.syntaxErrorAt(span,
					"invalid escape sequence \"\\%c\"", c))
			}
		}

		s = append(s, c)

		charLen++
		byteLen += utf8.RuneLen(c)

		t.skip(1)
	}

	span := NewSpanAt(start, charLen)

	return &Token{
		Type:  TokenTypeString,
		Span:  span,
		Data:  string(data[:byteLen]),
		Value: string(s),
	}
}

func (t *tokenizer) peekChar() rune {
	c, sz := utf8.DecodeRune(t.data)
	if c == utf8.RuneError {
		if sz == 0 {
			panic(t.syntaxError("truncated document"))
		} else {
			panic(t.syntaxError("invalid UTF-8 sequence"))
		}
	}

	return c
}

func (t *tokenizer) readChar() rune {
	c := t.peekChar()
	t.skip(1)
	return c
}

func (t *tokenizer) skip(n int64) {
	for range n {
		if c := t.peekChar(); c == '\r' && t.peekChar() == '\n' {
			t.data = t.data[2:]

			t.point.Offset += 2
			t.point.Line += 1
			t.point.Column = 1
		} else if c == '\n' {
			t.data = t.data[1:]

			t.point.Offset += 1
			t.point.Line += 1
			t.point.Column = 1
		} else {
			rlen := utf8.RuneLen(c)
			t.data = t.data[rlen:]

			t.point.Offset += rlen
			t.point.Column += 1
		}
	}
}

func (t *tokenizer) skipWhitespaces() {
	for len(t.data) > 0 {
		if c := t.peekChar(); !isWhitespaceChar(c) {
			return
		}

		t.skip(1)
	}
}

func (t *tokenizer) startsWithEOL() int {
	if len(t.data) == 0 {
		return 0
	}

	if t.data[0] == '\n' {
		return 1
	}

	if t.data[0] == '\r' {
		if len(t.data) < 2 {
			panic(t.syntaxError("truncated EOL sequence: " +
				"missing '\n' character"))
		}

		if t.data[1] != '\n' {
			panic(t.syntaxError("invalid EOL sequence: " +
				"missing '\n' character"))
		}

		return 2
	}

	return 0
}

func (t *tokenizer) skipEOL() int {
	if eolLen := t.startsWithEOL(); eolLen > 0 {
		t.skip(int64(eolLen))
		return eolLen
	}

	return 0
}

func (t *tokenizer) skipComment() {
	t.skip(1) // '#'

	for len(t.data) > 0 {
		t.peekChar()

		if t.startsWithEOL() > 0 {
			break
		}

		t.skip(1)
	}
}

func isWhitespaceChar(c rune) bool {
	return c == '\t' || c == ' '
}

func isWhitespaceOrEOLChar(c rune) bool {
	return isWhitespaceChar(c) || c == '\r' || c == '\n'
}

func isSymbolFirstChar(c rune) bool {
	return c >= 'a' && c <= 'z'
}

func isSymbolChar(c rune) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

func isWordBoundary(c rune) bool {
	return isWhitespaceOrEOLChar(c) || c == '{' || c == '}'
}
