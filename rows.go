package driver

import (
	"database/sql/driver"
	"io"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
)

type rows struct {
	iterator   *bigquery.RowIterator
	nextCalled bool
	prevValues []bigquery.Value
	prevErr    error
}

func (r *rows) schema() bigquery.Schema {
	// Must call next before we can access the schema.
	// Cache the result/error for later.
	if !r.nextCalled {
		r.prevValues, r.prevErr = r.next()
	}
	return r.iterator.Schema
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
	schema := r.schema()
	return string(schema[index].Type)
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

	for i := range dest {
		dest[i] = values[i]
	}
	return nil
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