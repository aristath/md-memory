package memory

import (
	"encoding"
	"errors"
	"reflect"

	"go.yaml.in/yaml/v3"
)

var errCyclicValue = errors.New("cyclic Go value cannot be encoded")

// YAML's Go encoder does not detect cycles. Track the active path, rather than
// all visited values, so ordinary shared maps, pointers, and slices still work.
func cyclicValue(value any) bool {
	type reference struct {
		typeOf  reflect.Type
		pointer uintptr
		length  int
	}
	active := map[reference]bool{}
	var visit func(reflect.Value) bool
	visit = func(v reflect.Value) bool {
		if !v.IsValid() {
			return false
		}
		if v.CanInterface() {
			switch v.Interface().(type) {
			case yaml.Marshaler, encoding.TextMarshaler:
				return false // These types supply their own encoded representation.
			}
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if v.IsNil() {
				return false
			}
			key := reference{typeOf: v.Type(), pointer: uintptr(v.UnsafePointer())}
			if v.Kind() == reflect.Slice {
				key.length = v.Len()
			}
			if active[key] {
				return true
			}
			active[key] = true
			defer delete(active, key)
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			return visit(v.Elem())
		case reflect.Map:
			entries := v.MapRange()
			for entries.Next() {
				if visit(entries.Key()) || visit(entries.Value()) {
					return true
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				if visit(v.Index(i)) {
					return true
				}
			}
		case reflect.Struct:
			if v.Type() == reflect.TypeFor[yaml.Node]() {
				return visit(v.FieldByName("Content")) // Alias pointers are valid YAML recursion.
			}
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if field.PkgPath == "" && field.Tag.Get("yaml") != "-" && visit(v.Field(i)) {
					return true
				}
			}
		}
		return false
	}
	return visit(reflect.ValueOf(value))
}
