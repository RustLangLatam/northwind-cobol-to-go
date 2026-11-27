package grpchandlers_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"milton_prism/core/services/nwc620/application"
	grpchandlers "milton_prism/core/services/nwc620/infrastructure/grpc_handlers"
	"milton_prism/core/services/nwc620/infrastructure/metrics"
	"milton_prism/core/services/nwc620/infrastructure/printer"
	"milton_prism/core/services/nwc620/infrastructure/repositories"
	"milton_prism/core/services/nwc620/mocks"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/db2fake"
	"milton_prism/pkg/db2access/ibmi"
	nwc620v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc620/v1"
)

type direct struct{ s *db2access.Store }

func (d direct) Do(fn func(*db2access.Store) error) error { return fn(d.s) }

type notReady struct{}

func (notReady) Do(func(*db2access.Store) error) error { return svcruntime.ErrNotReady }

type env struct {
	fake   *db2fake.Fake
	tables repositories.Tables
	dir    string
	client nwc620v1.Nwc620ServiceClient
}

var dallas, _ = time.LoadLocation("America/Chicago")

func newEnv(t *testing.T, runner func(*db2access.Store) repositories.StoreRunner) env {
	t.Helper()
	tables, err := repositories.NewTables()
	require.NoError(t, err)
	fake := db2fake.New()
	fake.Declare(tables.All()...)
	fake.ShuffleUnordered(7)
	cfg := db2access.Config{Host: "ibmi.example", Library: "NWLIB", User: "NWUSER",
		Password: "pw-for-tests", CommitMode: db2access.CommitNone, CCSID: 37}
	store, err := db2access.Open(context.Background(), ibmi.Driver{Connect: fake.ConnectFunc()}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	clock := mocks.FixedClock{T: time.Date(2026, 11, 25, 9, 30, 0, 0, dallas)}
	counters, err := metrics.New(prometheus.NewRegistry())
	require.NoError(t, err)
	app, err := application.NewService(repositories.NewDb2InvoiceRepository(runner(store), tables),
		&printer.DirPrinter{Dir: dir, Clock: clock}, clock, counters)
	require.NoError(t, err)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	nwc620v1.RegisterNwc620ServiceServer(gs, grpchandlers.NewNwc620Handler(app))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	e := env{fake: fake, tables: tables, dir: dir, client: nwc620v1.NewNwc620ServiceClient(conn)}
	e.seed(t)
	return e
}

func directRunner(s *db2access.Store) repositories.StoreRunner { return direct{s} }

func (e env) insert(t *testing.T, tbl db2access.Table, vals map[string]any) {
	t.Helper()
	set := make([]db2access.Assign, 0, len(vals))
	for k, v := range vals {
		set = append(set, db2access.Assign{Column: k, Value: v})
	}
	ins, err := tbl.Insert(set)
	require.NoError(t, err)
	require.NoError(t, e.fake.Seed(ins))
}

func (e env) seed(t *testing.T) {
	t.Helper()
	e.insert(t, e.tables.OrdHdr, map[string]any{"OHORD#": "10249", "OHCUST": "TOMSP", "OHODAT": "1000705", "OHSHPV": "1",
		"OHFRT": "11.61", "OHTOTL": "1863.40"})
	e.insert(t, e.tables.OrdHdr, map[string]any{"OHORD#": "10248", "OHCUST": "VINET", "OHODAT": "1000704", "OHSHPV": "3",
		"OHFRT": "32.38", "OHSNAM": "Vins et alcools Chevalier", "OHSADR": "59 rue de l'Abbaye", "OHSCTY": "Reims",
		"OHSCNT": "France", "OHTOTL": "440.00"})
	e.insert(t, e.tables.CusMst, map[string]any{"CMCUST": "VINET", "CMNAME": "Vins et alcools Chevalier",
		"CMADDR": "59 rue de l'Abbaye", "CMCITY": "Reims", "CMCNTY": "France"})
	e.insert(t, e.tables.CodTbl, map[string]any{"CTTYPE": "SHP", "CTCODE": "3", "CTDESC": "Federal Shipping"})
	e.insert(t, e.tables.CodTbl, map[string]any{"CTTYPE": "SHP", "CTCODE": "1", "CTDESC": "Speedy Express"})
	e.insert(t, e.tables.PrdMst, map[string]any{"PMPRD#": "11", "PMNAME": "Queso Cabrales"})
	e.insert(t, e.tables.PrdMst, map[string]any{"PMPRD#": "42", "PMNAME": "Singaporean Hokkien Fried Mee"})
	for _, l := range [][7]string{
		{"10248", "3", "72", "5", "34.80", "0.00", "174.00"},
		{"10249", "1", "14", "9", "18.60", "0.00", "167.40"},
		{"10248", "1", "11", "12", "14.00", "0.00", "168.00"},
		{"10248", "2", "42", "10", "9.80", "0.05", "93.10"},
	} {
		e.insert(t, e.tables.OrdDtl, map[string]any{"ODORD#": l[0], "ODLIN#": l[1], "ODPRD#": l[2],
			"ODQTY": l[3], "ODPRIC": l[4], "ODDISC": l[5], "ODEXT": l[6]})
	}
}

func (e env) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	require.NoError(t, err)
	out := make([]string, 0, len(entries))
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func call(e env, ord int32) (*nwc620v1.Nwc620Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.client.Nwc620(ctx, &nwc620v1.Nwc620Request{LkOrdNo: ord})
}

func TestNwc620_OK(t *testing.T) {
	t.Parallel()
	e := newEnv(t, directRunner)
	resp, err := call(e, 10248)
	require.NoError(t, err)
	assert.Equal(t, "0", resp.GetStatus())
	assert.Equal(t, int32(10248), resp.GetLkOrdNo())

	files := e.files(t)
	require.Len(t, files, 1)
	b, err := os.ReadFile(filepath.Join(e.dir, files[0]))
	require.NoError(t, err)
	doc := string(b)
	require.True(t, strings.HasPrefix(doc, "\fNORTHWIND TRADERS - DALLAS"), "leading page eject")
	pad := func(s string) string { return s + strings.Repeat(" ", 132-len(s)) }
	want := "\f" + pad("NORTHWIND TRADERS - DALLAS"+strings.Repeat(" ", 74)+"INVOICE   PAGE     1") +
		"\n" + pad("2101 COMMERCE ST.  DALLAS TX 75201"+strings.Repeat(" ", 66)+"DATE  11/25/26") +
		"\n\n" + pad("ORDER NO.   10248     ORDERED   07/04/00     SHIP VIA  Federal Shipping") +
		"\n\n" + pad("SOLD TO   VINET Vins et alcools Chevalier                   SHIP TO   Vins et alcools Chevalier") +
		"\n" + pad("                59 rue de l'Abbaye                                    59 rue de l'Abbaye") +
		"\n" + pad("                Reims           France                                Reims           France") +
		"\n\n" + pad("LN  PROD#  DESCRIPTION                                QTY     UNIT PRICE  DISC      EXTENDED") +
		"\n" + pad("  1    11  Queso Cabrales                               12         14.00    0%         168.00") +
		"\n" + pad("  2    42  Singaporean Hokkien Fried Mee                10          9.80    5%          93.10") +
		"\n" + pad("  3    72  *NOT ON FILE                                  5         34.80    0%         174.00") +
		"\n\n" + pad(strings.Repeat(" ", 64)+"ITEMS                  435.10") +
		"\n" + pad(strings.Repeat(" ", 64)+"FREIGHT                 32.38") +
		"\n\n" + pad(strings.Repeat(" ", 64)+"TOTAL DUE              472.38") + "\n"
	assert.Equal(t, want, doc)
}

func TestNwc620_NotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, directRunner)
	_, err := call(e, 10250)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, e.files(t), "no WRITE: no spooled file")
}

func TestNwc620_InvalidArgument(t *testing.T) {
	t.Parallel()
	e := newEnv(t, directRunner)
	for _, n := range []int32{0, -1, 10000000} {
		_, err := call(e, n)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "%d", n)
		assert.Contains(t, status.Convert(err).Message(), "1 to 9999999")
	}
	assert.Empty(t, e.files(t))
}

func TestNwc620_StoreErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		table string
		err   db2fake.DBError
		code  codes.Code
	}{
		{"ORDHDR", db2fake.DBError{SQLSTATE: "08S01", SQLCODE: -30081}, codes.Unavailable},
		{"ORDDTL", db2fake.DBError{SQLSTATE: "57033", SQLCODE: -913}, codes.Aborted},
		{"PRDMST", db2fake.DBError{SQLSTATE: "42501", SQLCODE: -551}, codes.Internal},
		{"CUSMST", db2fake.DBError{SQLSTATE: "40001", SQLCODE: -911}, codes.Aborted},
		{"CODTBL", db2fake.DBError{SQLSTATE: "08001", SQLCODE: -30080}, codes.Unavailable},
	} {
		e := newEnv(t, directRunner)
		e.fake.FailQuery(c.table, c.err)
		_, err := call(e, 10248)
		assert.Equal(t, c.code, status.Code(err), c.table)
		assert.NotContains(t, status.Convert(err).Message(), "SQL", "the cause stays on the server")
		assert.Empty(t, e.files(t))
	}
}

func TestNwc620_StoreNotReady(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(*db2access.Store) repositories.StoreRunner { return notReady{} })
	_, err := call(e, 10248)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestNwc620_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, directRunner)
	e.fake.SetLatency(3 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := e.client.Nwc620(ctx, &nwc620v1.Nwc620Request{LkOrdNo: 10248})
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

func TestNwc620_OutputUnavailable(t *testing.T) {
	t.Parallel()
	e := newEnv(t, directRunner)
	require.NoError(t, os.Remove(e.dir))
	_, err := call(e, 10248)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
