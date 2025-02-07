package bcl

import (
	"fmt"
)

type parser struct {
	source   string
	data     []byte
	lines    []string
	tokens   []*Token
	endPoint Point
}

func newParser(data []byte, source string) *parser {
	return &parser{
		source: source,
		data:   data,
		lines:  splitLines(data),
	}
}

func (p *parser) Parse() (doc *Document, err error) {
	defer func() {
		if v := recover(); v != nil {
			if verr, ok := v.(error); ok {
				err = ParseError{
					Err:   verr,
					Lines: p.lines,
				}
			} else {
				panic(v)
			}
		}
	}()

	tokenizer := newTokenizer(p.data, p.source)
	tokens := []*Token{}

	for {
		token := tokenizer.readToken()
		if token == nil {
			break
		}

		tokens = append(tokens, token)
	}

	p.tokens = tokens

	if len(tokens) > 0 {
		// Errors signaled because a syntaxic element is truncated must point at
		// something. We use a point just after the end of the last element.
		p.endPoint = tokens[len(tokens)-1].Span.End
		p.endPoint.Column++
	}

	elts := p.parseBlockContent(true)

	block := Block{
		Elements: elts,
	}

	topLevel := Element{
		Location: NewSpanAt(Point{0, 1, 1}, 0),
		Content:  &block,
	}

	doc = &Document{
		Source:   p.source,
		TopLevel: &topLevel,
	}

	return
}

func (p *parser) tokenSyntaxError(token *Token, format string, args ...any) error {
	return p.syntaxErrorAt(token.Span, format, args...)
}

func (p *parser) syntaxErrorAtPoint(point Point, format string, args ...any) error {
	return p.syntaxErrorAt(Span{point, point}, format, args...)
}

func (p *parser) syntaxErrorAt(span Span, format string, args ...any) error {
	return &SyntaxError{
		Source:      p.source,
		Location:    span,
		Description: fmt.Sprintf(format, args...),
	}
}

func (p *parser) duplicateErrorAt(elt, prevElt *Element) error {
	return &DuplicateError{
		Source:          p.source,
		Element:         elt,
		PreviousElement: prevElt,
	}
}

func (p *parser) peekToken() *Token {
	if len(p.tokens) == 0 {
		return nil
	}

	return p.tokens[0]
}

func (p *parser) readToken() *Token {
	if p.peekToken() == nil {
		return nil
	}

	return p.skipToken()
}

func (p *parser) skipToken() *Token {
	token := p.tokens[0]
	p.tokens = p.tokens[1:]
	return token
}

func (p *parser) skipEOL() int {
	var n int

	for len(p.tokens) > 0 {
		token := p.peekToken()
		if token.Type != TokenTypeEOL {
			break
		}

		p.tokens = p.tokens[1:]
		n++
	}

	return n
}

func (p *parser) parseElement() *Element {
	p.skipEOL()
	nameToken := p.readToken()
	if nameToken == nil {
		return nil
	}

	if nameToken.Type != TokenTypeSymbol {
		panic(p.tokenSyntaxError(nameToken, "invalid token %q, expected block "+
			"name or entry name", nameToken.Type))
	}

	valueToken := p.peekToken()
	if valueToken != nil {
		if valueToken.Type == TokenTypeString {
			p.skipToken()
		} else {
			valueToken = nil
		}
	}

	token := p.peekToken()
	if token != nil && token.Type == TokenTypeOpeningBracket {
		p.skipToken()

		elts := p.parseBlockContent(false)

		block := Block{
			Type:     nameToken.Value.(string),
			Elements: elts,
		}

		if valueToken != nil {
			block.Name = valueToken.Value.(string)
		}

		elt := Element{
			Location: nameToken.Span,
			Content:  &block,
		}

		if valueToken != nil {
			elt.Location = nameToken.Span.Union(valueToken.Span)
		}

		if p.skipEOL() > 1 {
			elt.FollowedByEmptyLine = true
		}

		return &elt
	}

	values := p.parseEntryValues()
	if valueToken != nil {
		values = append([]*Value{p.tokenValue(valueToken)}, values...)
	}

	entry := Entry{
		Name:   nameToken.Value.(string),
		Values: values,
	}

	elt := Element{
		Location: nameToken.Span,
		Content:  &entry,
	}

	if p.skipEOL() > 0 {
		elt.FollowedByEmptyLine = true
	}

	return &elt
}

func (p *parser) parseBlockContent(topLevel bool) []*Element {
	var elts []*Element

	blockTable := make(map[string]*Element)

	for {
		p.skipEOL()
		token := p.peekToken()

		if topLevel {
			if token == nil {
				break
			}
		} else {
			if token == nil {
				panic(p.syntaxErrorAtPoint(p.endPoint, "truncated block"))
			}

			if token.Type == TokenTypeClosingBracket {
				p.skipToken()
				break
			}
		}

		elt := p.parseElement()
		if elt == nil {
			panic(p.syntaxErrorAtPoint(p.endPoint, "truncated block"))
		}

		if block, ok := elt.Content.(*Block); ok {
			if block.Name != "" {
				id := elt.Id()

				if prevElt := blockTable[id]; prevElt != nil {
					panic(p.duplicateErrorAt(elt, prevElt))
				}

				blockTable[id] = elt
			}
		}

		elts = append(elts, elt)
	}

	return elts
}

func (p *parser) parseEntryValues() []*Value {
	var values []*Value

	for {
		token := p.readToken()
		if token == nil || token.Type == TokenTypeEOL {
			break
		}

		values = append(values, p.tokenValue(token))
	}

	return values
}

func (p *parser) tokenValue(t *Token) *Value {
	var v any

	switch t.Type {
	case TokenTypeSymbol:
		s := t.Value.(string)
		switch s {
		case "true":
			v = true
		case "false":
			v = false
		case "null":
			v = nil
		default:
			v = Symbol(t.Value.(string))
		}

	case TokenTypeString:
		v = t.Value.(string)

	case TokenTypeInteger:
		v = t.Value.(int64)

	case TokenTypeFloat:
		v = t.Value.(float64)

	default:
		panic(p.tokenSyntaxError(t, "invalid token %q, expected symbol, "+
			"string, integer or float", t.Type))
	}

	return &Value{
		Location: t.Span,
		Content:  v,
	}
}
