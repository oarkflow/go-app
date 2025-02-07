package bcl

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type ValidationError struct {
	Err      error
	Location *Span
}

type ValidationErrors struct {
	Errs  []ValidationError
	Lines []string
}

func (errs *ValidationErrors) Error() string {
	var buf bytes.Buffer

	for _, err := range errs.Errs {
		buf.WriteString("  - ")
		buf.WriteString(err.Err.Error())
		buf.WriteByte('\n')

		if err.Location != nil {
			err.Location.PrintSource(&buf, errs.Lines, "      ")
		}
	}

	return strings.TrimRight(buf.String(), "\n")
}

func (doc *Document) ValidationErrors() *ValidationErrors {
	var errs []ValidationError

	var walk func(*Element)
	walk = func(elt *Element) {
		for _, eltErr := range elt.validationErrors {
			verr := ValidationError{
				Err: eltErr,
			}

			if elt != doc.TopLevel {
				verr.Location = &elt.Location
			}

			var invalidValueErr *InvalidValueError
			if errors.As(eltErr, &invalidValueErr) {
				verr.Location = &invalidValueErr.Value.Location
			}

			errs = append(errs, verr)
		}

		if elt.readStatus == ElementReadStatusUnread {
			errs = append(errs, ValidationError{
				Err:      fmt.Errorf("invalid %s %q", elt.Type(), elt.Name()),
				Location: &elt.Location,
			})
		} else if elt.readStatus == ElementReadStatusIgnored {
			errs = append(errs, ValidationError{
				Err:      fmt.Errorf("ignored %s %q", elt.Type(), elt.Name()),
				Location: &elt.Location,
			})
		}

		if block, ok := elt.Content.(*Block); ok {
			for _, child := range block.Elements {
				walk(child)
			}
		}
	}

	walk(doc.TopLevel)

	if len(errs) == 0 {
		return nil
	}

	return &ValidationErrors{
		Errs:  errs,
		Lines: doc.lines,
	}
}

func (elt *Element) AddValidationError(err error) error {
	elt.validationErrors = append(elt.validationErrors, err)
	return err
}

type SimpleValidationError struct {
	Description string
}

func (err *SimpleValidationError) Error() string {
	return err.Description
}

func (elt *Element) AddSimpleValidationError(format string, args ...any) error {
	return elt.AddValidationError(&SimpleValidationError{
		Description: fmt.Sprintf(format, args...),
	})
}

type MissingElementError struct {
	ElementType *ElementType
	Names       []string
}

func (err *MissingElementError) Error() string {
	names := make([]string, len(err.Names))
	for i, name := range err.Names {
		names[i] = fmt.Sprintf("%q", name)
	}

	if err.ElementType == nil {
		return fmt.Sprintf("block must contain an element named %s or a "+
			"block of this type", WordsEnumerationOr(names))
	} else if *err.ElementType == ElementTypeBlock {
		return fmt.Sprintf("block must contain a block of type %s",
			WordsEnumerationOr(names))
	} else {
		return fmt.Sprintf("block must contain %s named %s",
			WordWithArticle(string(*err.ElementType)),
			WordsEnumerationOr(names))
	}
}

func (elt *Element) AddMissingElementError(eltType *ElementType, names []string) error {
	return elt.AddValidationError(&MissingElementError{
		ElementType: eltType,
		Names:       names,
	})
}

type InvalidElementTypeError struct {
	ExpectedType ElementType
}

func (err *InvalidElementTypeError) Error() string {
	return fmt.Sprintf("element should be %s",
		WordWithArticle(string(err.ExpectedType)))
}

func (elt *Element) AddInvalidElementTypeError(expectedType ElementType) error {
	return elt.AddValidationError(&InvalidElementTypeError{
		ExpectedType: expectedType,
	})
}

type MissingBlockNameError struct {
}

func (err *MissingBlockNameError) Error() string {
	return "missing or empty block name"
}

func (elt *Element) AddMissingBlockNameError() error {
	return elt.AddValidationError(&MissingBlockNameError{})
}

type ElementConflictError struct {
	ElementType  *ElementType
	ElementNames []string
	Names        []string
}

func (err *ElementConflictError) Error() string {
	eltNames := make([]string, len(err.ElementNames))
	for i, name := range err.ElementNames {
		eltNames[i] = fmt.Sprintf("%q", name)
	}

	names := make([]string, len(err.Names))
	for i, name := range err.Names {
		names[i] = fmt.Sprintf("%q", name)
	}

	if err.ElementType == nil {
		return fmt.Sprintf("block contains %s %s but must only "+
			"contain one element %s",
			PluralizeWord("element", len(eltNames)),
			WordsEnumerationAnd(eltNames), WordsEnumerationOr(names))
	} else if *err.ElementType == ElementTypeBlock {
		return fmt.Sprintf("block contains blocks of type %s but must only "+
			"contain one block of type %s",
			WordsEnumerationAnd(eltNames), WordsEnumerationOr(names))
	} else {
		return fmt.Sprintf("block contains entries named %s but must only "+
			"contain one entry named %s",
			WordsEnumerationAnd(eltNames), WordsEnumerationOr(names))
	}
}

func (elt *Element) AddElementConflictError(eltType *ElementType, eltNames, names []string) error {
	return elt.AddValidationError(&ElementConflictError{
		ElementType:  eltType,
		ElementNames: eltNames,
		Names:        names,
	})
}

type InvalidEntryNbValuesError struct {
	NbValues         int
	ExpectedNbValues []int
}

func (err *InvalidEntryNbValuesError) Error() string {
	ns := make([]string, len(err.ExpectedNbValues))
	for i, n := range err.ExpectedNbValues {
		ns[i] = strconv.Itoa(n)
	}

	return fmt.Sprintf("entry has %d %s but should have %s %s",
		err.NbValues, PluralizeWord("value", err.NbValues),
		WordsEnumerationOr(ns), PluralizeWord("value", len(ns)))
}

func (elt *Element) AddInvalidEntryNbValuesError(expectedNbValues ...int) error {
	return elt.AddValidationError(&InvalidEntryNbValuesError{
		NbValues:         len(elt.Content.(*Entry).Values),
		ExpectedNbValues: expectedNbValues,
	})
}

type InvalidEntryMinNbValuesError struct {
	NbValues int
	Min      int
}

func (err *InvalidEntryMinNbValuesError) Error() string {
	return fmt.Sprintf("entry has %d %s but should have at least %d %s",
		err.NbValues, PluralizeWord("value", err.NbValues),
		err.Min, PluralizeWord("value", err.Min))
}

func (elt *Element) AddInvalidEntryMinNbValuesError(min int) error {
	return elt.AddValidationError(&InvalidEntryMinNbValuesError{
		NbValues: len(elt.Content.(*Entry).Values),
		Min:      min,
	})
}

type InvalidEntryMinMaxNbValuesError struct {
	NbValues int
	Min      int
	Max      int
}

func (err *InvalidEntryMinMaxNbValuesError) Error() string {
	return fmt.Sprintf("entry has %d %s but should have between %d and %d %s",
		err.NbValues, PluralizeWord("value", err.NbValues),
		err.Min, err.Max, PluralizeWord("value", err.Max))
}

func (elt *Element) AddInvalidEntryMinMaxNbValuesError(min, max int) error {
	return elt.AddValidationError(&InvalidEntryMinMaxNbValuesError{
		NbValues: len(elt.Content.(*Entry).Values),
		Min:      min,
		Max:      max,
	})
}

type InvalidValueError struct {
	Value *Value
	Err   error
}

func (err *InvalidValueError) Unwrap() error {
	return err.Err
}

func (err *InvalidValueError) Error() string {
	return err.Err.Error()
}

func (elt *Element) AddInvalidValueError(v *Value, err error) error {
	return elt.AddValidationError(&InvalidValueError{
		Value: v,
		Err:   err,
	})
}

type InvalidValueTypeError struct {
	Type          ValueType
	ExpectedTypes []ValueType
}

func (err *InvalidValueTypeError) Error() string {
	etWithArticles := make([]string, len(err.ExpectedTypes))
	for i, et := range err.ExpectedTypes {
		etWithArticles[i] = WordWithArticle(string(et))
	}

	return fmt.Sprintf("value is %s but should be %s",
		WordWithArticle(string(err.Type)), WordsEnumerationOr(etWithArticles))
}

type MinIntegerValueError struct {
	Min int64
}

func (err *MinIntegerValueError) Error() string {
	return fmt.Sprintf("integer must be greater or equal to %d", err.Min)
}

type MaxIntegerValueError struct {
	Max int64
}

func (err *MaxIntegerValueError) Error() string {
	return fmt.Sprintf("integer must be lower or equal to %d", err.Max)
}

type MinMaxIntegerValueError struct {
	Min int64
	Max int64
}

func (err *MinMaxIntegerValueError) Error() string {
	return fmt.Sprintf("integer must be between %d and %d", err.Min, err.Max)
}
