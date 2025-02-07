package bcl

import (
	"fmt"
	"reflect"
)

type ElementReader interface {
	ReadBCLElement(*Element) error
}

func (elt *Element) Elements(name string, dest any) bool {
	return extractElements(elt.FindElements(name), dest)
}

func (elt *Element) Element(name string, dest any) bool {
	child := elt.MustFindElement(name)
	if child == nil {
		return false
	}

	if err := child.Extract(dest); err != nil {
		child.AddValidationError(err)
		return false
	}

	return true
}

func (elt *Element) MaybeElement(name string, dest any) bool {
	child := elt.FindElement(name)
	if child == nil {
		return true
	}

	if err := child.Extract(dest); err != nil {
		child.AddValidationError(err)
		return false
	}

	return true
}

func (elt *Element) Blocks(name string, dest any) bool {
	return extractElements(elt.FindBlocks(name), dest)
}

func (elt *Element) Block(btype string, dest any) bool {
	block := elt.MustFindBlock(btype)
	if block == nil {
		return false
	}

	if err := block.Extract(dest); err != nil {
		block.AddValidationError(err)
		return false
	}

	return true
}

func (elt *Element) MaybeBlock(btype string, dest any) bool {
	block := elt.FindBlock(btype)
	if block == nil {
		return true
	}

	if err := block.Extract(dest); err != nil {
		block.AddValidationError(err)
		return false
	}

	return true
}

func (elt *Element) Extract(dest any) error {
	if er, ok := dest.(ElementReader); ok {
		return er.ReadBCLElement(elt)
	}

	dv := reflect.ValueOf(dest)
	if dv.Kind() == reflect.Pointer && dv.Elem().Kind() == reflect.Pointer {
		dest2 := reflect.New(dv.Elem().Type().Elem())

		if err := elt.Extract(dest2.Interface()); err != nil {
			return err
		}

		dv.Elem().Set(dest2)
		return nil
	}

	panic(fmt.Sprintf("cannot extract element to destination of type %T", dest))
}

func extractElements(elts []*Element, dest any) bool {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer || dv.Elem().Kind() != reflect.Slice {
		panic(fmt.Sprintf("cannot extract elements to a value of type %T",
			dest))
	}

	t := dv.Elem().Type().Elem()
	slice := reflect.MakeSlice(reflect.SliceOf(t), 0, 0)

	valid := true

	for _, child := range elts {
		value := reflect.New(t)

		if err := child.Extract(value.Interface()); err != nil {
			child.AddValidationError(err)
		}

		slice = reflect.Append(slice, value.Elem())
	}

	if !valid {
		return false
	}

	dv.Elem().Set(slice)
	return true
}
