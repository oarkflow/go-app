package bcl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
)

type ParseError struct {
	Err   error
	Lines []string
}

func (err ParseError) Error() string {
	const indent = "  "

	var buf bytes.Buffer

	fmt.Fprintln(&buf, err.Err)

	var syntaxErr *SyntaxError
	var duplicateErr *DuplicateError

	if errors.As(err, &syntaxErr) {
		syntaxErr.Location.PrintSource(&buf, err.Lines, indent)
	} else if errors.As(err, &duplicateErr) {
		duplicateErr.Element.Location.PrintSource(&buf, err.Lines, indent)
	}

	return strings.TrimRight(buf.String(), "\n")
}

func (err ParseError) Unwrap() error {
	return err.Err
}

type SyntaxError struct {
	Source      string
	Location    Span
	Description string
}

func (err *SyntaxError) Error() string {
	msg := err.Location.String() + ": " + err.Description
	if err.Source != "" {
		msg = err.Source + ":" + msg
	}
	return msg
}

type DuplicateError struct {
	Source string

	Element         *Element
	PreviousElement *Element
}

func (err *DuplicateError) Error() string {
	eltType := err.Element.Type()
	description := fmt.Sprintf("duplicate %s %q, previous %s found line %d",
		eltType, err.Element.Id(), eltType, err.Element.Location.Start.Line)

	msg := err.Element.Location.String() + ": " + description
	if err.Source != "" {
		msg = err.Source + ":" + msg
	}

	return msg
}

type Point struct {
	Offset int // counted in characters (runes), not in bytes
	Line   int
	Column int
}

func (p Point) Cmp(p2 Point) int {
	var ret int

	switch {
	case p.Offset < p2.Offset:
		ret = -1
	case p.Offset > p2.Offset:
		ret = 1
	case p.Offset == p2.Offset:
		ret = 0
	}

	return ret
}

func MinPoint(p1, p2 Point) Point {
	var p Point

	switch p.Cmp(p2) {
	case -1:
		p = p1
	case 0:
		p = p1
	case 1:
		p = p2
	}

	return p
}

func MaxPoint(p1, p2 Point) Point {
	var p Point

	switch p.Cmp(p2) {
	case -1:
		p = p2
	case 0:
		p = p1
	case 1:
		p = p1
	}

	return p
}

func (p Point) String() string {
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

func (p Point) Equal(p2 Point) bool {
	return p.Offset == p2.Offset
}

type Span struct {
	Start Point
	End   Point
}

func (s Span) Union(s2 Span) Span {
	return Span{MinPoint(s.Start, s2.Start), MaxPoint(s.End, s2.End)}
}

func NewSpanAt(start Point, len int) Span {
	end := Point{
		Offset: start.Offset + len - 1,
		Line:   start.Line,
		Column: start.Column + len - 1,
	}

	return Span{
		Start: start,
		End:   end,
	}
}

func (s Span) Len() int {
	return s.End.Offset - s.Start.Offset + 1
}

func (s Span) String() string {
	if p, ok := s.Point(); ok {
		return p.String()
	} else {
		return fmt.Sprintf("%v-%v", s.Start, s.End)
	}
}

func (s Span) Point() (Point, bool) {
	if s.Start.Equal(s.End) {
		return s.Start, true
	}

	return Point{}, false
}

func (s Span) PrintSource(w io.Writer, lines []string, indent string) {
	const context = 2

	nbLineDigits := int(math.Floor(math.Log10(float64(len(lines)))) + 1)

	printLine := func(l int) {
		fmt.Fprintf(w, "%s%*d │ ", indent, nbLineDigits, l+1)
		fmt.Fprintln(w, lines[l])
	}

	lstart := s.Start.Line - 1
	lend := s.End.Line - 1

	for l := max(lstart-context, 0); l < lstart; l++ {
		printLine(l)
	}

	for l := lstart; l <= lend; l++ {
		printLine(l)

		line := lines[l]

		cstart := 0
		if l == lstart {
			cstart = s.Start.Column - 1
		}

		cend := len(line)
		if l == lend {
			cend = s.End.Column
		}

		fmt.Fprintf(w, "%s%*s │ ", indent, nbLineDigits, " ")

		for c := 0; c < len(line); c++ {
			char := ' '
			if c >= cstart && c < cend {
				char = '^'
			}

			fmt.Fprint(w, string(char))
		}

		// The final point can appear just after the end of the last line
		if l == lend && cend > len(line) {
			fmt.Fprint(w, string('^'))
		}

		fmt.Fprintln(w)
	}

	for l := lend + 1; l < min(lend+context+1, len(lines)); l++ {
		printLine(l)
	}
}

var lineRE = regexp.MustCompile("\r?\n")

func splitLines(data []byte) []string {
	lines := lineRE.Split(string(data), -1)
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
