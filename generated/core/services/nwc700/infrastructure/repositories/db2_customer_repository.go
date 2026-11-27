// Package repositories reads the NWC700 tables through db2access.
package repositories

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/ports"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
)

var _ ports.CustomerRepository = (*Db2CustomerRepository)(nil)

var tracer = otel.Tracer("milton_prism/core/services/nwc700/repositories")

// StoreRunner runs fn with the connected store (svcruntime.Holder does).
type StoreRunner interface {
	Do(fn func(*db2access.Store) error) error
	Ready(ctx context.Context) error
}

// CUSMST columns, from the schema facts (DDS qddssrc/cusmst.pf): every field is
// NOT NULL WITH DEFAULT (blanks or zeros, no DFT).
var cusmstColumns = []struct{ name, sqlType, dft string }{
	{"CMCUST", "CHAR(5)", "BLANKS"},
	{"CMNAME", "CHAR(40)", "BLANKS"},
	{"CMCONT", "CHAR(30)", "BLANKS"},
	{"CMTITL", "CHAR(30)", "BLANKS"},
	{"CMADDR", "CHAR(60)", "BLANKS"},
	{"CMCITY", "CHAR(15)", "BLANKS"},
	{"CMREGN", "CHAR(15)", "BLANKS"},
	{"CMPOST", "CHAR(10)", "BLANKS"},
	{"CMCNTY", "CHAR(15)", "BLANKS"},
	{"CMPHON", "CHAR(24)", "BLANKS"},
	{"CMFAX", "CHAR(24)", "BLANKS"},
	{"CMSTAT", "CHAR(1)", "BLANKS"},
	{"CMCRDT", "NUMERIC(6, 0)", "ZEROS"},
	{"CMCHDT", "NUMERIC(6, 0)", "ZEROS"},
	{"CMCHUS", "CHAR(10)", "BLANKS"},
}

// CustomerTable declares CUSMST in schema (the library) with its unique key CMCUST.
func CustomerTable(schema string) (db2access.Table, error) {
	cols := make([]db2access.Column, 0, len(cusmstColumns))
	for _, c := range cusmstColumns {
		col, err := db2access.ColumnFromFacts(c.name, c.sqlType, "NOT_NULL", true, c.dft)
		if err != nil {
			return db2access.Table{}, fmt.Errorf("nwc700: declare CUSMST.%s: %w", c.name, err)
		}
		cols = append(cols, col)
	}
	t, err := db2access.NewTable(schema, "CUSMST", cols...)
	if err != nil {
		return db2access.Table{}, fmt.Errorf("nwc700: declare CUSMST: %w", err)
	}
	t, err = t.WithUniqueKey([]string{"CMCUST"})
	if err != nil {
		return db2access.Table{}, fmt.Errorf("nwc700: declare the CUSMST key: %w", err)
	}
	return t, nil
}

// Db2CustomerRepository reads CUSMST.
type Db2CustomerRepository struct {
	store StoreRunner
	table db2access.Table
}

// NewDb2CustomerRepository builds the repository over a declared CUSMST.
func NewDb2CustomerRepository(store StoreRunner, table db2access.Table) *Db2CustomerRepository {
	return &Db2CustomerRepository{store: store, table: table}
}

// ListByCountry selects the six fields the list prints, sorted on the host.
func (r *Db2CustomerRepository) ListByCountry(ctx context.Context, limit int) ([]domain.Customer, error) {
	ctx, span := tracer.Start(ctx, "db2.CUSMST.select")
	defer span.End()
	span.SetAttributes(attribute.String("db.system", "db2"), attribute.String("db.sql.table", r.table.Qualified()))

	sel := db2access.Select{
		Table:   r.table,
		Columns: []string{"CMCNTY", "CMCITY", "CMNAME", "CMCUST", "CMCONT", "CMPHON"},
		// PORT-NOTE: [CMCUST breaks ties between customers with the same country, city and name] The COBOL SORT leaves equal keys in an undefined order; the unique key as a last sort key makes every run print the same order.
		OrderBy: []db2access.Order{{Column: "CMCNTY"}, {Column: "CMCITY"}, {Column: "CMNAME"}, {Column: "CMCUST"}},
		Limit:   limit,
	}
	var rows []db2access.Row
	err := r.store.Do(func(s *db2access.Store) error {
		var qerr error
		rows, qerr = s.Query(ctx, sel)
		return qerr
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "select failed")
		return nil, classify(err)
	}
	out := make([]domain.Customer, 0, len(rows))
	for _, row := range rows {
		c, err := customerFrom(row)
		if err != nil {
			span.SetStatus(codes.Error, "row mapping failed")
			return nil, err
		}
		out = append(out, c)
	}
	span.SetAttributes(attribute.Int("db.rows", len(out)))
	return out, nil
}

// Ping is the readiness check: the store is connected and answers a one-row read.
func (r *Db2CustomerRepository) Ping(ctx context.Context) error {
	if err := r.store.Ready(ctx); err != nil {
		return domain.StoreUnavailable(err)
	}
	ctx, span := tracer.Start(ctx, "db2.CUSMST.ping")
	defer span.End()
	err := r.store.Do(func(s *db2access.Store) error {
		_, qerr := s.Query(ctx, db2access.Select{Table: r.table, Columns: []string{"CMCUST"}, Limit: 1})
		return qerr
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ping failed")
		return classify(err)
	}
	return nil
}

func customerFrom(row db2access.Row) (domain.Customer, error) {
	var c domain.Customer
	for _, f := range []struct {
		col string
		dst *string
	}{
		{"CMCUST", &c.Number}, {"CMNAME", &c.Name}, {"CMCONT", &c.Contact},
		{"CMCITY", &c.City}, {"CMCNTY", &c.Country}, {"CMPHON", &c.Phone},
	} {
		v, err := row.Text(f.col)
		if err != nil {
			return domain.Customer{}, fmt.Errorf("nwc700: read CUSMST.%s: %w", f.col, err)
		}
		*f.dst = v
	}
	return c, nil
}

// classify turns a store failure into the service's error kinds. A context error is
// kept as it is, so the caller sees Canceled or DeadlineExceeded.
func classify(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("nwc700: CUSMST read: %w", err)
	case errors.Is(err, svcruntime.ErrNotReady):
		return domain.StoreUnavailable(err)
	case errors.Is(err, db2access.ErrLimitTooLarge):
		return domain.ReadLimitTooLow(err)
	case errors.Is(err, db2access.ErrResultTooLarge), errors.Is(err, db2access.ErrResultMemoryBusy):
		return domain.ResultTooLarge(err)
	}
	var dbErr *db2access.Error
	if errors.As(err, &dbErr) {
		switch {
		case dbErr.Retryable():
			return domain.StoreBusy(err)
		case len(dbErr.SQLState) == 5 && dbErr.SQLState[:2] == "08":
			return domain.StoreUnavailable(err)
		}
	}
	return fmt.Errorf("nwc700: CUSMST read failed: %w", err)
}

// IsPermanentOpenError tells the store holder which open errors retrying cannot fix:
// a rejected sign-on, missing authority or a missing table.
func IsPermanentOpenError(err error) bool {
	var dbErr *db2access.Error
	if !errors.As(err, &dbErr) {
		return false
	}
	switch dbErr.SQLCode {
	case -30082, -551, -552, -204, -7008:
		return true
	}
	switch dbErr.SQLState {
	case "28000", "42501", "42704", "55019":
		return true
	}
	return false
}
