package bcl

import (
	"fmt"
)

type ValueType string

const (
	ValueTypeSymbol  ValueType = "symbol"
	ValueTypeBool    ValueType = "bool"
	ValueTypeString  ValueType = "string"
	ValueTypeInteger ValueType = "integer"
	ValueTypeFloat   ValueType = "float"
)

type Value struct {
	Location Span
	Content  any // either Symbol, bool, string, int64 or float64
}

type Symbol string

func (v *Value) Type() (t ValueType) {
	switch v.Content.(type) {
	case Symbol:
		t = ValueTypeSymbol
	case bool:
		t = ValueTypeBool
	case string:
		t = ValueTypeString
	case int64:
		t = ValueTypeInteger
	case float64:
		t = ValueTypeFloat

	default:
		panic(fmt.Sprintf("unhandled value %#v (%T)", v.Content, v.Content))
	}

	return
}
