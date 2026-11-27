package repositories_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/infrastructure/repositories"
	"milton_prism/core/shared/domainerr"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/db2fake"
	"milton_prism/pkg/db2access/ibmi"
)

type direct struct{ s *db2access.Store }

func (d direct) Do(fn func(*db2access.Store) error) error { return fn(d.s) }

type notReady struct{}

func (notReady) Do(func(*db2access.Store) error) error { return svcruntime.ErrNotReady }

type env struct {
	fake   *db2fake.Fake
	tables repositories.Tables
	repo   *repositories.Db2InvoiceRepository
}

func newEnv(t *testing.T, mode db2access.CommitMode) env {
	t.Helper()
	tables, err := repositories.NewTables()
	require.NoError(t, err)
	fake := db2fake.New()
	fake.Declare(tables.All()...)
	fake.ShuffleUnordered(42)
	cfg := db2access.Config{Host: "ibmi.example", Library: "NWLIB", User: "NWUSER",
		Password: "pw-for-tests", CommitMode: mode, CCSID: 37}
	store, err := db2access.Open(context.Background(), ibmi.Driver{Connect: fake.ConnectFunc()}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return env{fake: fake, tables: tables, repo: repositories.NewDb2InvoiceRepository(direct{store}, tables)}
}

func seed(t *testing.T, e env, tbl db2access.Table, vals map[string]any) {
	t.Helper()
	set := make([]db2access.Assign, 0, len(vals))
	for k, v := range vals {
		set = append(set, db2access.Assign{Column: k, Value: v})
	}
	ins, err := tbl.Insert(set)
	require.NoError(t, err)
	require.NoError(t, e.fake.Seed(ins))
}

func dec(t *testing.T, p cobolnum.Pic, s string) cobolnum.Dec {
	t.Helper()
	d, sizeErr, err := p.ParseDec(s, cobolnum.Truncation)
	require.NoError(t, err)
	require.False(t, sizeErr)
	return d
}

func seedOrders(t *testing.T, e env) {
	t.Helper()
	for _, h := range []map[string]any{
		{"OHORD#": "10249", "OHCUST": "TOMSP", "OHODAT": "1000102", "OHSHPV": "1", "OHFRT": "11.61", "OHTOTL": "1863.40"},
		{"OHORD#": "10248", "OHCUST": "VINET", "OHODAT": "991231", "OHSHPV": "3", "OHFRT": "32.38",
			"OHSNAM": "Vins et alcools Chevalier", "OHSADR": "59 rue de l'Abbaye", "OHSCTY": "Reims", "OHSCNT": "France",
			"OHTOTL": "440.00", "OHSTAT": "S"},
		{"OHORD#": "10247", "OHCUST": "ALFKI", "OHODAT": "0", "OHSHPV": "2", "OHFRT": "1.00", "OHTOTL": "5.00"},
	} {
		seed(t, e, e.tables.OrdHdr, h)
	}
	for _, l := range [][7]string{
		{"10248", "3", "72", "5", "34.80", "0.00", "174.00"},
		{"10247", "1", "1", "9", "1.00", "0.00", "9.00"},
		{"10248", "1", "11", "12", "14.00", "0.05", "159.60"},
		{"10249", "1", "14", "9", "18.60", "0.00", "167.40"},
		{"10248", "2", "42", "10", "9.80", "0.00", "98.00"},
		{"10248", "-1", "99", "1", "1.00", "0.00", "1.00"},
	} {
		seed(t, e, e.tables.OrdDtl, map[string]any{"ODORD#": l[0], "ODLIN#": l[1], "ODPRD#": l[2],
			"ODQTY": l[3], "ODPRIC": l[4], "ODDISC": l[5], "ODEXT": l[6]})
	}
}

func TestGetOrderHeader_OK(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seedOrders(t, e)
	h, found, err := e.repo.GetOrderHeader(context.Background(), dec(t, domain.PicOrderNo, "10248"))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "10248", h.OrderNo.Plain())
	assert.Equal(t, "VINET", h.Customer)
	assert.Equal(t, "991231", h.OrderDate.Plain())
	assert.Equal(t, "3", h.ShipVia)
	assert.Equal(t, "32.38", h.Freight.Plain())
	assert.Equal(t, "440.00", h.Total.Plain())
	assert.Equal(t, "Reims", trim(h.ShipCity))
	assert.Equal(t, domain.PicFreight, h.Freight.Pic())
}

func trim(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func TestGetOrderHeader_NotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seedOrders(t, e)
	_, found, err := e.repo.GetOrderHeader(context.Background(), dec(t, domain.PicOrderNo, "10250"))
	require.NoError(t, err)
	assert.False(t, found)
}

func TestListOrderLines_RangeInKeyOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitCS) // reads only: no table to journal
	seedOrders(t, e)
	lines, err := e.repo.ListOrderLines(context.Background(), dec(t, domain.PicOrderNo, "10248"))
	require.NoError(t, err)
	require.Len(t, lines, 3) // line -1 is below START KEY NOT LESS THAN (order, 0); 10247 and 10249 are other orders
	for i, want := range []string{"1", "2", "3"} {
		assert.Equal(t, want, lines[i].LineNo.Plain())
	}
	assert.Equal(t, "11", lines[0].ProductNo.Plain())
	assert.Equal(t, "12", lines[0].Qty.Plain())
	assert.Equal(t, "14.00", lines[0].Price.Plain())
	assert.Equal(t, "0.05", lines[0].Disc.Plain())
	assert.Equal(t, "159.60", lines[0].Extended.Plain())
	stmts := e.fake.Statements()
	require.NotEmpty(t, stmts)
	assert.Contains(t, stmts[len(stmts)-1], "ORDER BY")
	assert.Contains(t, stmts[len(stmts)-1], "FETCH FIRST 1000 ROWS ONLY")
}

func TestListOrderLines_None(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seedOrders(t, e)
	lines, err := e.repo.ListOrderLines(context.Background(), dec(t, domain.PicOrderNo, "10250"))
	require.NoError(t, err)
	assert.Empty(t, lines)
}

func TestGetCustomer_OKAndNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seed(t, e, e.tables.CusMst, map[string]any{"CMCUST": "ALFKI", "CMNAME": "Alfreds Futterkiste", "CMCITY": "Berlin"})
	seed(t, e, e.tables.CusMst, map[string]any{"CMCUST": "VINET", "CMNAME": "Vins et alcools Chevalier",
		"CMADDR": "59 rue de l'Abbaye", "CMCITY": "Reims", "CMCNTY": "France", "CMCRDT": "123199"})
	c, found, err := e.repo.GetCustomer(context.Background(), "VINET")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "Vins et alcools Chevalier", trim(c.Name))
	assert.Equal(t, "France", trim(c.Country))
	_, found, err = e.repo.GetCustomer(context.Background(), "NOONE")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestGetCodeDescription_OKAndNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seed(t, e, e.tables.CodTbl, map[string]any{"CTTYPE": "SHP", "CTCODE": "1", "CTDESC": "Speedy Express"})
	seed(t, e, e.tables.CodTbl, map[string]any{"CTTYPE": "REG", "CTCODE": "3", "CTDESC": "Region three"})
	seed(t, e, e.tables.CodTbl, map[string]any{"CTTYPE": "SHP", "CTCODE": "3", "CTDESC": "Federal Shipping"})
	desc, found, err := e.repo.GetCodeDescription(context.Background(), "SHP", "3         ")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "Federal Shipping", trim(desc))
	_, found, err = e.repo.GetCodeDescription(context.Background(), "SHP", "9         ")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestGetProductName_OKAndNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seed(t, e, e.tables.PrdMst, map[string]any{"PMPRD#": "42", "PMNAME": "Singaporean Hokkien Fried Mee", "PMPRIC": "9.80"})
	seed(t, e, e.tables.PrdMst, map[string]any{"PMPRD#": "11", "PMNAME": "Queso Cabrales"})
	name, found, err := e.repo.GetProductName(context.Background(), dec(t, domain.PicProductNo, "42"))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "Singaporean Hokkien Fried Mee", trim(name))
	_, found, err = e.repo.GetProductName(context.Background(), dec(t, domain.PicProductNo, "77"))
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSeed_DuplicateKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seed(t, e, e.tables.OrdHdr, map[string]any{"OHORD#": "10248"})
	ins, err := e.tables.OrdHdr.Insert([]db2access.Assign{{Column: "OHORD#", Value: "10248"}})
	require.NoError(t, err)
	err = e.fake.Seed(ins)
	var dbErr *db2access.Error
	require.ErrorAs(t, db2access.Classify("seed", err), &dbErr)
	assert.Equal(t, "23505", dbErr.SQLState)
}

func TestGetCustomer_NotRepresentable(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	_, _, err := e.repo.GetCustomer(context.Background(), "€URO")
	require.Error(t, err)
	assert.True(t, domainerr.Is(err, domainerr.InvalidArgument))
	code, _ := svcruntime.Classify(err)
	assert.Equal(t, codes.InvalidArgument, code)
	var dbErr *db2access.Error
	require.ErrorAs(t, err, &dbErr)
	assert.Equal(t, db2access.SQLStateNotRepresentable, dbErr.SQLState)
}

// Every port method maps the database failures to the same codes.
func TestRepository_ErrorPaths(t *testing.T) {
	t.Parallel()
	calls := map[string]func(r *repositories.Db2InvoiceRepository) error{
		"ORDHDR": func(r *repositories.Db2InvoiceRepository) error {
			_, _, err := r.GetOrderHeader(context.Background(), domain.PicOrderNo.Zero())
			return err
		},
		"CUSMST": func(r *repositories.Db2InvoiceRepository) error {
			_, _, err := r.GetCustomer(context.Background(), "VINET")
			return err
		},
		"CODTBL": func(r *repositories.Db2InvoiceRepository) error {
			_, _, err := r.GetCodeDescription(context.Background(), "SHP", "1")
			return err
		},
		"ORDDTL": func(r *repositories.Db2InvoiceRepository) error {
			_, err := r.ListOrderLines(context.Background(), domain.PicOrderNo.Zero())
			return err
		},
		"PRDMST": func(r *repositories.Db2InvoiceRepository) error {
			_, _, err := r.GetProductName(context.Background(), domain.PicProductNo.Zero())
			return err
		},
	}
	failures := []struct {
		name string
		err  db2fake.DBError
		code codes.Code
		kind domainerr.Kind
	}{
		{"lock timeout", db2fake.DBError{SQLSTATE: "57033", SQLCODE: -913}, codes.Aborted, domainerr.Aborted},
		{"connection lost", db2fake.DBError{SQLSTATE: "08S01", SQLCODE: -30081}, codes.Unavailable, domainerr.Unavailable},
		{"not authorized", db2fake.DBError{SQLSTATE: "42501", SQLCODE: -551}, codes.Internal, domainerr.Internal},
	}
	for table, call := range calls {
		for _, f := range failures {
			t.Run(table+" "+f.name, func(t *testing.T) {
				t.Parallel()
				e := newEnv(t, db2access.CommitNone)
				e.fake.FailQuery(table, f.err)
				err := call(e.repo)
				require.Error(t, err)
				code, _ := svcruntime.Classify(err)
				assert.Equal(t, f.code, code)
				k, _ := domainerr.KindOf(err)
				assert.Equal(t, f.kind, k)
				var dbErr *db2access.Error
				assert.ErrorAs(t, err, &dbErr)
			})
		}
		t.Run(table+" store not ready", func(t *testing.T) {
			t.Parallel()
			tables, err := repositories.NewTables()
			require.NoError(t, err)
			err = call(repositories.NewDb2InvoiceRepository(notReady{}, tables))
			assert.ErrorIs(t, err, svcruntime.ErrNotReady)
			code, _ := svcruntime.Classify(err)
			assert.Equal(t, codes.Unavailable, code)
		})
	}
}

func TestRepository_Timeout(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	seedOrders(t, e)
	e.fake.SetLatency(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := e.repo.GetOrderHeader(ctx, dec(t, domain.PicOrderNo, "10248"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	code, _ := svcruntime.Classify(err)
	assert.Equal(t, codes.DeadlineExceeded, code)
}

func TestPing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, db2access.CommitNone)
	require.NoError(t, e.repo.Ping(context.Background()))
	e.fake.FailQuery("CODTBL", db2fake.DBError{SQLSTATE: "08001", SQLCODE: -30080})
	assert.Error(t, e.repo.Ping(context.Background()))
}

func TestNewTables_UniqueKeys(t *testing.T) {
	t.Parallel()
	tables, err := repositories.NewTables()
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"ODORD#", "ODLIN#"}}, tables.OrdDtl.UniqueKeys())
	assert.Equal(t, [][]string{{"CTTYPE", "CTCODE"}}, tables.CodTbl.UniqueKeys())
	assert.Len(t, tables.All(), 5)
}
