package bcl

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"time"
)

type ValueReader interface {
	ReadBCLValue(*Value) error
}

func (v *Value) Extract(dest any) error {
	vt := v.Type()

	if vr, ok := dest.(ValueReader); ok {
		return vr.ReadBCLValue(v)
	}

	switch ptr := dest.(type) {
	case *bool:
		switch vt {
		case ValueTypeBool:
			*ptr = v.Content.(bool)
		default:
			return NewValueTypeError(v, ValueTypeBool)
		}

	case *string:
		switch vt {
		case ValueTypeString:
			*ptr = v.Content.(string)
		case ValueTypeSymbol:
			*ptr = string(v.Content.(Symbol))
		default:
			return NewValueTypeError(v, ValueTypeString, ValueTypeSymbol)
		}

	case *int:
		switch vt {
		case ValueTypeInteger:
			i := v.Content.(int64)
			min := int64(math.MinInt)
			max := int64(math.MaxInt)
			if i < min || i > max {
				return NewMinMaxIntegerValueError(min, max)
			}
			*ptr = int(i)
		default:
			return NewValueTypeError(v, ValueTypeInteger)
		}

	case *int64:
		switch vt {
		case ValueTypeInteger:
			*ptr = v.Content.(int64)
		default:
			return NewValueTypeError(v, ValueTypeInteger)
		}

	case *float64:
		switch vt {
		case ValueTypeFloat:
			*ptr = v.Content.(float64)
		case ValueTypeInteger:
			i := v.Content.(int64)
			min := int64(-1) << 53
			max := int64(1) << 53
			if i < min || i > max {
				return NewMinMaxIntegerValueError(min, max)
			}
			*ptr = float64(i)
		default:
			return NewValueTypeError(v, ValueTypeFloat, ValueTypeInteger)
		}

	case *time.Duration:
		switch vt {
		case ValueTypeInteger:
			i := v.Content.(int64)
			if i < 0 {
				return errors.New("invalid negative duration")
			}
			*ptr = time.Duration(i) * time.Second
		case ValueTypeFloat:
			f := v.Content.(float64)
			if f < 0.0 {
				return errors.New("invalid negative duration")
			}
			*ptr = time.Duration(f * float64(time.Second))
		default:
			return NewValueTypeError(v, ValueTypeString)
		}

	case **regexp.Regexp:
		switch vt {
		case ValueTypeString:
			re, err := regexp.Compile(v.Content.(string))
			if err != nil {
				return fmt.Errorf("invalid regexp: %w", err)
			}
			*ptr = re
		default:
			return NewValueTypeError(v, ValueTypeString)
		}

	default:
		// Given a type T, there are two possible destination values:
		//
		// 1. A value of type *T if the caller wants to extract the BCL value to
		// a stack-allocated value.
		//
		// 2. A value of type **T if the caller wants to extract the BCL value to
		// a heap-allocated value (or in most cases because the value is
		// optional, hence the pointer type).
		//
		//
		// 1 was handled at the beginning of the fonction (ReadBCLValue will
		// always have a pointer receiver).
		//
		// 2 is handled here: we allocate a new value and call Extract again.

		dv := reflect.ValueOf(dest)
		if dv.Kind() == reflect.Pointer && dv.Elem().Kind() == reflect.Pointer {
			dest2 := reflect.New(dv.Elem().Type().Elem())

			if err := v.Extract(dest2.Interface()); err != nil {
				return err
			}

			dv.Elem().Set(dest2)
			return nil
		}

		panic(fmt.Sprintf("cannot extract value to destination of type %T",
			dest))
	}

	return nil
}

func NewValueTypeError(v *Value, expectedTypes ...ValueType) *InvalidValueTypeError {
	return &InvalidValueTypeError{Type: v.Type(), ExpectedTypes: expectedTypes}
}

func NewMinIntegerValueError(min int64) *MinIntegerValueError {
	return &MinIntegerValueError{Min: min}
}

func NewMaxIntegerValueError(max int64) *MaxIntegerValueError {
	return &MaxIntegerValueError{Max: max}
}

func NewMinMaxIntegerValueError(min, max int64) *MinMaxIntegerValueError {
	return &MinMaxIntegerValueError{Min: min, Max: max}
}

type ValueValidationFunc func(any) error

type ValidatableValue struct {
	Dest           any
	ValidationFunc ValueValidationFunc
}

func (v *ValidatableValue) ReadBCLValue(value *Value) error {
	if err := value.Extract(v.Dest); err != nil {
		return err
	}

	dv := reflect.ValueOf(v.Dest)
	if dv.Kind() != reflect.Pointer {
		panic(fmt.Sprintf("unhandled non-pointer value destination of type %T",
			v.Dest))
	}

	return v.ValidationFunc(dv.Elem().Interface())
}

func WithValueValidation(dest any, fn ValueValidationFunc) *ValidatableValue {
	return &ValidatableValue{
		Dest:           dest,
		ValidationFunc: fn,
	}
}

func ValidatePositiveInteger(v any) error {
	// This kind of function is a good example of how badly designed Go is

	var i int64
	var set bool

	switch iv := v.(type) {
	case int:
		i = int64(iv)
		set = true
	case int8:
		i = int64(iv)
		set = true
	case int16:
		i = int64(iv)
		set = true
	case int32:
		i = int64(iv)
		set = true
	case int64:
		i = int64(iv)
		set = true

	case *int:
		if iv != nil {
			i = int64(*iv)
			set = true
		}
	case *int8:
		if iv != nil {
			i = int64(*iv)
			set = true
		}
	case *int16:
		if iv != nil {
			i = int64(*iv)
			set = true
		}
	case *int32:
		if iv != nil {
			i = int64(*iv)
			set = true
		}
	case *int64:
		if iv != nil {
			i = int64(*iv)
			set = true
		}
	}

	if set && i <= 0 {
		return fmt.Errorf("integer must be greater than zero")
	}

	return nil
}
