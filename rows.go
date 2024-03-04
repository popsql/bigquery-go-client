package driver

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
	_ driver.RowsColumnTypeLength           = (*rows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*rows)(nil)

	// TODO:
	// _ driver.RowsColumnTypeScanType         = (*rows)(nil)
)

type rows struct {
	iterator   *bigquery.RowIterator
	nextCalled bool
	prevValues []bigquery.Value
	prevErr    error
}

func (r *rows) Columns() []string {
	schema := r.schema()
	columns := make([]string, len(schema))
	for i, field := range schema {
		columns[i] = field.Name
	}
	return columns
}

func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	field := r.schema()[index]
	return string(field.Type)
}

func (r *rows) ColumnTypeLength(index int) (int64, bool) {
	field := r.schema()[index]
	ok := field.MaxLength != 0
	return field.MaxLength, ok
}

func (r *rows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	field := r.schema()[index]
	ok := field.Precision != 0 || field.Scale != 0
	return field.Precision, field.Scale, ok
}

func (r *rows) Close() error {
	var vals []bigquery.Value
	for r.iterator.Next(&vals) == nil {
		// Drain iterator
	}
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	values, err := r.prevOrNext()
	if err != nil {
		return err
	}

	schema := r.schema()
	for idx := range dest {
		value, err := convertValue(schema[idx], values[idx])
		if err != nil {
			return err
		}
		dest[idx] = value
	}
	return nil
}

func (r *rows) schema() bigquery.Schema {
	// Must call next before we can access the schema.
	// Cache the result/error for later.
	if !r.nextCalled {
		r.prevValues, r.prevErr = r.next()
	}
	return r.iterator.Schema
}

func (r *rows) prevOrNext() ([]bigquery.Value, error) {
	if r.prevValues != nil || r.prevErr != nil {
		values, err := r.prevValues, r.prevErr
		r.prevValues, r.prevErr = nil, nil
		return values, err
	}
	return r.next()
}

func (r *rows) next() ([]bigquery.Value, error) {
	r.nextCalled = true

	var values []bigquery.Value
	if err := r.iterator.Next(&values); err != nil {
		if err == iterator.Done {
			return nil, io.EOF
		}
		return nil, err
	}
	return values, nil
}

func convertValue(field *bigquery.FieldSchema, value bigquery.Value) (driver.Value, error) {
	val, err := convertValueHelper(field, value)
	if err != nil {
		return nil, err
	}

	if driver.IsValue(val) {
		return val, nil
	}

	// Marshal ARRAY and RECORD types to JSON, since arrays/maps aren't
	// valid driver.Value types (but []byte is).
	out, err := json.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("error marshalling repeated field to JSON: %w", err)
	}
	return out, nil
}

func convertValueHelper(field *bigquery.FieldSchema, value bigquery.Value) (any, error) {
	if field.Repeated {
		return convertRepeatedType(field, value)
	}
	return convertUnitType(field, value)
}

func convertUnitType(field *bigquery.FieldSchema, value bigquery.Value) (any, error) {
	switch field.Type {
	case bigquery.StringFieldType:
		return convertBasicType[string](field, value)
	case bigquery.BytesFieldType:
		return convertBasicType[[]byte](field, value)
	case bigquery.IntegerFieldType:
		return convertBasicType[int64](field, value)
	case bigquery.FloatFieldType:
		return convertBasicType[float64](field, value)
	case bigquery.BooleanFieldType:
		return convertBasicType[bool](field, value)
	case bigquery.TimestampFieldType:
		return convertBasicType[time.Time](field, value)
	case bigquery.DateFieldType:
		return convertStringType[civil.Date](field, value)
	case bigquery.TimeFieldType:
		return convertStringType[civil.Time](field, value)
	case bigquery.DateTimeFieldType:
		return convertStringType[civil.DateTime](field, value)
	case bigquery.NumericFieldType:
		return convertRationalType(field, value, bigquery.NumericString)
	case bigquery.BigNumericFieldType:
		return convertRationalType(field, value, bigquery.BigNumericString)
	case bigquery.GeographyFieldType:
		return convertBasicType[string](field, value)
	case bigquery.IntervalFieldType:
		return convertBasicType[string](field, value)
	case bigquery.JSONFieldType:
		return convertBasicType[string](field, value)
	case bigquery.RangeFieldType:
		return convertBasicType[string](field, value)
	case bigquery.RecordFieldType:
		return convertRecordType(field, value)
	default:
		return nil, &InvalidFieldTypeError{
			FieldType: field.Type,
		}
	}
}

func convertRepeatedType(field *bigquery.FieldSchema, value bigquery.Value) ([]any, error) {
	switch val := value.(type) {
	case nil:
		return nil, nil
	case []bigquery.Value:
		a := make([]any, len(val))
		for i, v := range val {
			av, err := convertUnitType(field, v)
			if err != nil {
				return nil, err
			}
			a[i] = av
		}
		return a, nil
	default:
		return nil, &UnexpectedTypeError{
			FieldType: field.Type,
			Expected:  reflect.TypeFor[[]bigquery.Value](),
			Actual:    val,
		}
	}
}

func convertRecordType(field *bigquery.FieldSchema, value bigquery.Value) (map[string]any, error) {
	switch val := value.(type) {
	case nil:
		return nil, nil
	case []bigquery.Value:
		m := map[string]any{}
		for i, mf := range field.Schema {
			mv, err := convertValueHelper(mf, val[i])
			if err != nil {
				return nil, err
			}
			m[mf.Name] = mv
		}
		return m, nil
	default:
		return nil, &UnexpectedTypeError{
			FieldType: field.Type,
			Expected:  reflect.TypeFor[[]bigquery.Value](),
			Actual:    val,
		}
	}
}

func convertBasicType[T any](field *bigquery.FieldSchema, value bigquery.Value) (driver.Value, error) {
	switch val := value.(type) {
	case nil:
		return nil, nil
	case T:
		return val, nil
	default:
		return nil, &UnexpectedTypeError{
			FieldType: field.Type,
			Expected:  reflect.TypeFor[T](),
			Actual:    val,
		}
	}
}

func convertStringType[T fmt.Stringer](field *bigquery.FieldSchema, value bigquery.Value) (driver.Value, error) {
	switch val := value.(type) {
	case nil:
		return nil, nil
	case T:
		return val.String(), nil
	default:
		return nil, &UnexpectedTypeError{
			FieldType: field.Type,
			Expected:  reflect.TypeFor[T](),
			Actual:    val,
		}
	}
}

type ratToStr func(*big.Rat) string

func convertRationalType(field *bigquery.FieldSchema, value bigquery.Value, toStr ratToStr) (driver.Value, error) {
	switch val := value.(type) {
	case nil:
		return nil, nil
	case *big.Rat:
		// Attempt to use the minimum number of digits after the decimal point,
		// if the resulting number will be exact.
		if prec, exact := val.FloatPrec(); exact {
			return val.FloatString(prec), nil
		}

		// Otherwise, fallback to default string conversion function, which
		// uses the maximum number of digits supported by BigQuery.
		return toStr(val), nil
	default:
		return nil, &UnexpectedTypeError{
			FieldType: field.Type,
			Expected:  reflect.TypeFor[*big.Rat](),
			Actual:    val,
		}
	}
}
