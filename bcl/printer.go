package bcl

import (
	"fmt"
	"io"
	"strconv"
)

type printer struct {
	w     io.Writer
	doc   *Document
	level int
}

func newPrinter(w io.Writer, doc *Document) *printer {
	return &printer{
		w:   w,
		doc: doc,
	}
}

func (p *printer) Print() (err error) {
	defer func() {
		if v := recover(); v != nil {
			if verr, ok := v.(error); ok {
				err = verr
				return
			}

			panic(v)
		}
	}()

	p.printDocument()
	return
}

func (p *printer) printDocument() {
	block := p.doc.TopLevel.Content.(*Block)

	for _, child := range block.Elements {
		p.printElement(child)
	}
}

func (p *printer) printElement(elt *Element) {
	switch v := elt.Content.(type) {
	case *Block:
		p.printBlock(v)
	case *Entry:
		p.printEntry(v)
	}

	if elt.FollowedByEmptyLine {
		p.print("\n")
	}
}

func (p *printer) printBlock(block *Block) {
	p.printIndent()

	p.print(block.Type)

	if block.Name != "" {
		p.print(" ")
		p.printStringValue(block.Name)
	}

	p.print(" {\n")

	p.level++
	for _, elt := range block.Elements {
		p.printElement(elt)
	}
	p.level--

	p.printIndent()
	p.print("}\n")
}

func (p *printer) printEntry(entry *Entry) {
	p.printIndent()

	p.print(entry.Name)

	for _, value := range entry.Values {
		p.print(" ")
		p.printValue(value)
	}

	p.print("\n")
}

func (p *printer) printValue(value *Value) {
	switch v := value.Content.(type) {
	case Symbol:
		p.print(string(v))

	case bool:
		if v {
			p.print("true")
		} else {
			p.print("false")
		}

	case string:
		p.printStringValue(v)

	case int64:
		p.print(strconv.FormatInt(v, 10))

	case float64:
		p.print(strconv.FormatFloat(v, 'f', -1, 64))

	default:
		panic(fmt.Sprintf("unhandled value %#v (%T)", value, value))
	}
}

func (p *printer) printStringValue(s string) {
	p.print("\"")

	for _, c := range s {
		switch c {
		case '\a', '\b', '\t', '\n', '\v', '\f', '\r', '"', '\\':
			p.print("\\")
			p.print(string(c))

		default:
			p.print(string(c))
		}
	}

	p.print("\"")
}

func (p *printer) print(s string) {
	if _, err := p.w.Write([]byte(s)); err != nil {
		panic(err)
	}
}

func (p *printer) printIndent() {
	for range p.level {
		p.print("  ")
	}
}
