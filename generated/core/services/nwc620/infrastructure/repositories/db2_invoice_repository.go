package repositories

import (
	"context"
	"errors"
	"fmt"

	"github.com/RustLangLatam/cobolnum-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/ports"
	"milton_prism/pkg/db2access"
)

var _ ports.InvoiceRepository = (*Db2InvoiceRepository)(nil)

// StoreRunner runs fn with the connected store (svcruntime.Holder[*db2access.Store]).
type StoreRunner interface {
	Do(fn func(*db2access.Store) error) error
}

// maxOrderLines bounds the read of one order's lines: ODLIN# is 3P 0 and the read
// starts at line 0, so an order has at most 1000 lines (0 to 999).
const maxOrderLines = 1000

// Db2InvoiceRepository reads ORDHDR, ORDDTL, CUSMST, PRDMST and CODTBL.
type Db2InvoiceRepository struct {
	store  StoreRunner
	tables Tables
}

// NewDb2InvoiceRepository returns the repository over the store.
func NewDb2InvoiceRepository(store StoreRunner, tables Tables) *Db2InvoiceRepository {
	return &Db2InvoiceRepository{store: store, tables: tables}
}

var tracer = otel.Tracer("milton_prism/core/services/nwc620/repositories")

// query runs one SELECT inside a span named after the file.
func (r *Db2InvoiceRepository) query(ctx context.Context, op string, sel db2access.Select) ([]db2access.Row, error) {
	ctx, span := tracer.Start(ctx, "db2 "+op)
	defer span.End()
	span.SetAttributes(attribute.String("db.system", "db2"), attribute.String("db.sql.table", sel.Table.Name()))
	var rows []db2access.Row
	err := r.store.Do(func(s *db2access.Store) error {
		var qerr error
		rows, qerr = s.Query(ctx, sel)
		return qerr
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")
		return nil, classify(op, err)
	}
	return rows, nil
}

// classify gives a database failure its domain kind. A context error keeps its own
// kind (Canceled, DeadlineExceeded) through %w.
func classify(op string, err error) error {
	var dbErr *db2access.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("nwc620: %s: %w", op, err)
	case errors.As(err, &dbErr) && dbErr.SQLState == db2access.SQLStateNotRepresentable:
		return domain.ErrTextNotStorable(fmt.Errorf("nwc620: %s: %w", op, err))
	case errors.As(err, &dbErr) && dbErr.Retryable():
		return domain.ErrStoreBusy(fmt.Errorf("nwc620: %s: %w", op, err))
	case errors.As(err, &dbErr) && len(dbErr.SQLState) == 5 && dbErr.SQLState[:2] == "08":
		return domain.ErrStoreUnavailable(fmt.Errorf("nwc620: %s: %w", op, err))
	}
	// svcruntime.ErrNotReady and every other failure keep their chain: StatusError
	// maps the store-not-connected case to Unavailable and the rest to Internal.
	return fmt.Errorf("nwc620: %s: %w", op, err)
}

func text(row db2access.Row, col string, dst *string) error {
	v, err := row.Text(col)
	if err != nil {
		return fmt.Errorf("column %s: %w", col, err)
	}
	*dst = v
	return nil
}

func exact(row db2access.Row, col string, dst *cobolnum.Dec) error {
	v, err := row.Exact(col)
	if err != nil {
		return fmt.Errorf("column %s: %w", col, err)
	}
	if !v.HasDec {
		return fmt.Errorf("column %s: the value has no cobolnum decimal", col)
	}
	*dst = v.Dec
	return nil
}

// GetOrderHeader is READ ORDHDR (random, key OHORD#).
func (r *Db2InvoiceRepository) GetOrderHeader(ctx context.Context, orderNo cobolnum.Dec) (domain.OrderHeader, bool, error) {
	rows, err := r.query(ctx, "read ORDHDR", db2access.Select{Table: r.tables.OrdHdr,
		Columns: []string{"OHORD#", "OHCUST", "OHODAT", "OHSHPV", "OHFRT", "OHSNAM", "OHSADR", "OHSCTY", "OHSCNT", "OHTOTL"},
		Where:   []db2access.Cond{{Column: "OHORD#", Op: db2access.Eq, Value: orderNo}},
		Limit:   1})
	if err != nil || len(rows) == 0 {
		return domain.OrderHeader{}, false, err
	}
	var h domain.OrderHeader
	row := rows[0]
	err = errors.Join(
		exact(row, "OHORD#", &h.OrderNo), text(row, "OHCUST", &h.Customer), exact(row, "OHODAT", &h.OrderDate),
		text(row, "OHSHPV", &h.ShipVia), exact(row, "OHFRT", &h.Freight), text(row, "OHSNAM", &h.ShipName),
		text(row, "OHSADR", &h.ShipAddress), text(row, "OHSCTY", &h.ShipCity), text(row, "OHSCNT", &h.ShipCountry),
		exact(row, "OHTOTL", &h.Total))
	if err != nil {
		return domain.OrderHeader{}, false, domain.ErrInternal(fmt.Errorf("nwc620: read ORDHDR: %w", err))
	}
	return h, true, nil
}

// GetCustomer is READ CUSMST (random, key CMCUST).
func (r *Db2InvoiceRepository) GetCustomer(ctx context.Context, customer string) (domain.Customer, bool, error) {
	rows, err := r.query(ctx, "read CUSMST", db2access.Select{Table: r.tables.CusMst,
		Columns: []string{"CMNAME", "CMADDR", "CMCITY", "CMCNTY"},
		Where:   []db2access.Cond{{Column: "CMCUST", Op: db2access.Eq, Value: customer}},
		Limit:   1})
	if err != nil || len(rows) == 0 {
		return domain.Customer{}, false, err
	}
	var c domain.Customer
	row := rows[0]
	if err := errors.Join(text(row, "CMNAME", &c.Name), text(row, "CMADDR", &c.Address),
		text(row, "CMCITY", &c.City), text(row, "CMCNTY", &c.Country)); err != nil {
		return domain.Customer{}, false, domain.ErrInternal(fmt.Errorf("nwc620: read CUSMST: %w", err))
	}
	return c, true, nil
}

// GetCodeDescription is READ CODTBL (random, key CTTYPE, CTCODE).
func (r *Db2InvoiceRepository) GetCodeDescription(ctx context.Context, codeType, code string) (string, bool, error) {
	rows, err := r.query(ctx, "read CODTBL", db2access.Select{Table: r.tables.CodTbl,
		Columns: []string{"CTDESC"},
		Where: []db2access.Cond{
			{Column: "CTTYPE", Op: db2access.Eq, Value: codeType},
			{Column: "CTCODE", Op: db2access.Eq, Value: code},
		},
		Limit: 1})
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	var desc string
	if err := text(rows[0], "CTDESC", &desc); err != nil {
		return "", false, domain.ErrInternal(fmt.Errorf("nwc620: read CODTBL: %w", err))
	}
	return desc, true, nil
}

// ListOrderLines is START ORDDTL KEY NOT LESS THAN (orderNo, 0) and READ NEXT while
// ODORD# = orderNo.
func (r *Db2InvoiceRepository) ListOrderLines(ctx context.Context, orderNo cobolnum.Dec) ([]domain.OrderLine, error) {
	rows, err := r.query(ctx, "read ORDDTL", db2access.Select{Table: r.tables.OrdDtl,
		Columns: []string{"ODLIN#", "ODPRD#", "ODQTY", "ODPRIC", "ODDISC", "ODEXT"},
		Where: []db2access.Cond{
			{Column: "ODORD#", Op: db2access.Eq, Value: orderNo},
			{Column: "ODLIN#", Op: db2access.Ge, Value: "0"},
		},
		OrderBy: []db2access.Order{{Column: "ODLIN#"}},
		Limit:   maxOrderLines})
	if err != nil {
		return nil, err
	}
	out := make([]domain.OrderLine, 0, len(rows))
	for _, row := range rows {
		var l domain.OrderLine
		if err := errors.Join(exact(row, "ODLIN#", &l.LineNo), exact(row, "ODPRD#", &l.ProductNo),
			exact(row, "ODQTY", &l.Qty), exact(row, "ODPRIC", &l.Price), exact(row, "ODDISC", &l.Disc),
			exact(row, "ODEXT", &l.Extended)); err != nil {
			return nil, domain.ErrInternal(fmt.Errorf("nwc620: read ORDDTL: %w", err))
		}
		out = append(out, l)
	}
	return out, nil
}

// GetProductName is READ PRDMST (random, key PMPRD#).
func (r *Db2InvoiceRepository) GetProductName(ctx context.Context, productNo cobolnum.Dec) (string, bool, error) {
	rows, err := r.query(ctx, "read PRDMST", db2access.Select{Table: r.tables.PrdMst,
		Columns: []string{"PMNAME"},
		Where:   []db2access.Cond{{Column: "PMPRD#", Op: db2access.Eq, Value: productNo}},
		Limit:   1})
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	var name string
	if err := text(rows[0], "PMNAME", &name); err != nil {
		return "", false, domain.ErrInternal(fmt.Errorf("nwc620: read PRDMST: %w", err))
	}
	return name, true, nil
}

// Ping is the readiness probe: a one-row read of CODTBL.
func (r *Db2InvoiceRepository) Ping(ctx context.Context) error {
	_, err := r.query(ctx, "ping", db2access.Select{Table: r.tables.CodTbl, Columns: []string{"CTTYPE"}, Limit: 1})
	return err
}
