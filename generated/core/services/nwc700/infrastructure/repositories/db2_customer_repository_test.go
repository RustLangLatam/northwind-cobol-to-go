package repositories_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/infrastructure/repositories"
	"milton_prism/core/shared/domainerr"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/db2fake"
	"milton_prism/pkg/db2access/ibmi"
)

const library = "NWLIB"

func testConfig() db2access.Config {
	return db2access.Config{Host: "ibmi.example", Library: library, User: "NWUSER",
		Password: db2access.Secret("pw-for-tests"), CommitMode: db2access.CommitCS, CCSID: 37}
}

type fixture struct {
	fake  *db2fake.Fake
	table db2access.Table
	store *svcruntime.Holder[*db2access.Store]
	repo  *repositories.Db2CustomerRepository
}

func newFixture(t *testing.T, cfg db2access.Config) *fixture {
	t.Helper()
	table, err := repositories.CustomerTable(library)
	require.NoError(t, err)
	fake := db2fake.New()
	fake.Declare(table)
	fake.ShuffleUnordered(7)
	holder := &svcruntime.Holder[*db2access.Store]{}
	holder.Start(context.Background(), func(ctx context.Context) (*db2access.Store, error) {
		// The service writes no table, so no table is passed for the journaling check.
		return db2access.Open(ctx, ibmi.Driver{Connect: fake.ConnectFunc()}, cfg)
	}, 10*time.Millisecond)
	t.Cleanup(func() { _ = holder.Close(context.Background()) })
	require.Eventually(t, func() bool { return holder.Ready(context.Background()) == nil }, 5*time.Second, 5*time.Millisecond)
	return &fixture{fake: fake, table: table, store: holder, repo: repositories.NewDb2CustomerRepository(holder, table)}
}

func (f *fixture) seed(t *testing.T, no, name, city, country, status string) {
	t.Helper()
	ins, err := f.table.Insert([]db2access.Assign{
		{Column: "CMCUST", Value: no}, {Column: "CMNAME", Value: name}, {Column: "CMCITY", Value: city},
		{Column: "CMCNTY", Value: country}, {Column: "CMCONT", Value: "CONTACT " + no},
		{Column: "CMPHON", Value: "PHONE " + no}, {Column: "CMSTAT", Value: status},
	})
	require.NoError(t, err)
	require.NoError(t, f.fake.Seed(ins))
}

func TestCustomerTable_Declaration(t *testing.T) {
	t.Parallel()
	table, err := repositories.CustomerTable(library)
	require.NoError(t, err)
	assert.Equal(t, "NWLIB.CUSMST", table.Qualified())
	assert.Len(t, table.Columns(), 15)
	assert.Equal(t, [][]string{{"CMCUST"}}, table.UniqueKeys())
	_, err = repositories.CustomerTable("BAD LIB")
	assert.Error(t, err)
}

func TestListByCountry_SortedByCountryCityName(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	// Arrival sequence is not the print order; a deleted (D) customer is listed too.
	f.seed(t, "WOLZA", "WOLSKI", "WARSZAWA", "POLAND", "")
	f.seed(t, "BLAUS", "BLAUER SEE", "MANNHEIM", "GERMANY", "")
	f.seed(t, "ALFKI", "ALFREDS", "BERLIN", "GERMANY", "D")
	f.seed(t, "KOENE", "KOENIGLICH", "BRANDENBURG", "GERMANY", "")
	f.seed(t, "QUICK", "QUICK STOP", "CUNEWALDE", "GERMANY", "")
	f.seed(t, "DRACD", "DRACHENBLUT", "AACHEN", "GERMANY", "")
	f.seed(t, "ANATR", "ANA TRUJILLO", "MEXICO D.F.", "MEXICO", "")
	f.seed(t, "ANTON", "ANTONIO MORENO", "MEXICO D.F.", "MEXICO", "")

	got, err := f.repo.ListByCountry(context.Background(), domain.MaxCustomers+1)
	require.NoError(t, err)
	var nos []string
	for _, c := range got {
		nos = append(nos, strings.TrimSpace(c.Number))
	}
	assert.Equal(t, []string{"DRACD", "ALFKI", "KOENE", "QUICK", "BLAUS", "ANATR", "ANTON", "WOLZA"}, nos)
	assert.Equal(t, "ALFREDS", strings.TrimSpace(got[1].Name))
	assert.Equal(t, "BERLIN", strings.TrimSpace(got[1].City))
	assert.Equal(t, "CONTACT ALFKI", strings.TrimSpace(got[1].Contact))
	assert.Equal(t, "PHONE ALFKI", strings.TrimSpace(got[1].Phone))

	stmts := strings.Join(f.fake.Statements(), "\n")
	assert.Contains(t, stmts, `ORDER BY "CMCNTY", "CMCITY", "CMNAME", "CMCUST"`)
	assert.Contains(t, stmts, "FETCH FIRST 100000 ROWS ONLY")
}

func TestListByCountry_EmptyFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	got, err := f.repo.ListByCountry(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListByCountry_LimitBoundsTheRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	f.seed(t, "CCCCC", "C", "C", "C", "")
	f.seed(t, "AAAAA", "A", "A", "A", "")
	f.seed(t, "BBBBB", "B", "B", "B", "")
	got, err := f.repo.ListByCountry(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "AAAAA", got[0].Number)
	assert.Equal(t, "BBBBB", got[1].Number)
}

func TestCustomerTable_DuplicateKeyRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	f.seed(t, "ALFKI", "A", "A", "A", "")
	ins, err := f.table.Insert([]db2access.Assign{{Column: "CMCUST", Value: "ALFKI"}})
	require.NoError(t, err)
	err = f.fake.Seed(ins)
	var dbErr *db2access.Error
	require.True(t, errors.As(db2access.Classify("seed", err), &dbErr))
	assert.Equal(t, "23505", dbErr.SQLState)
}

func TestListByCountry_ErrorPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind domainerr.Kind
	}{
		{"lock timeout", db2fake.DBError{SQLSTATE: "57033", SQLCODE: -913}, domainerr.Aborted},
		{"deadlock", db2fake.DBError{SQLSTATE: "40001", SQLCODE: -911}, domainerr.Aborted},
		{"connection lost", db2fake.DBError{SQLSTATE: "08S01", SQLCODE: -30080}, domainerr.Unavailable},
		{"not authorized", db2fake.DBError{SQLSTATE: "42501", SQLCODE: -551}, domainerr.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, testConfig())
			f.fake.FailQuery("NWLIB.CUSMST", tc.err)
			_, err := f.repo.ListByCountry(context.Background(), 10)
			require.Error(t, err)
			kind, _ := domainerr.KindOf(err)
			assert.Equal(t, tc.kind, kind)
			var dbErr *db2access.Error
			assert.True(t, errors.As(err, &dbErr))
		})
	}
}

func TestListByCountry_Timeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	f.fake.SetLatency(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := f.repo.ListByCountry(ctx, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	kind, _ := domainerr.KindOf(err)
	assert.Equal(t, domainerr.Internal, kind, "a deadline keeps its own status")
}

func TestListByCountry_LimitAboveMaxLimit(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.MaxLimit = 1000
	f := newFixture(t, cfg)
	_, err := f.repo.ListByCountry(context.Background(), domain.MaxCustomers+1)
	assert.True(t, domainerr.Is(err, domainerr.FailedPrecondition))
	assert.ErrorIs(t, err, db2access.ErrLimitTooLarge)
}

func TestListByCountry_StoreNotReady(t *testing.T) {
	t.Parallel()
	table, err := repositories.CustomerTable(library)
	require.NoError(t, err)
	repo := repositories.NewDb2CustomerRepository(&svcruntime.Holder[*db2access.Store]{}, table)
	_, err = repo.ListByCountry(context.Background(), 10)
	assert.True(t, domainerr.Is(err, domainerr.Unavailable))
	assert.ErrorIs(t, err, svcruntime.ErrNotReady)
	assert.True(t, domainerr.Is(repo.Ping(context.Background()), domainerr.Unavailable))
}

func TestPing_OK(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	assert.NoError(t, f.repo.Ping(context.Background()))
}

func TestPing_QueryFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, testConfig())
	f.fake.FailQuery("NWLIB.CUSMST", db2fake.DBError{SQLSTATE: "08001", SQLCODE: -30081})
	assert.True(t, domainerr.Is(f.repo.Ping(context.Background()), domainerr.Unavailable))
}

func TestIsPermanentOpenError(t *testing.T) {
	t.Parallel()
	perm := func(state string, code int) error {
		return db2access.Classify("open", db2fake.DBError{SQLSTATE: state, SQLCODE: code})
	}
	assert.True(t, repositories.IsPermanentOpenError(perm("28000", -30082)))
	assert.True(t, repositories.IsPermanentOpenError(perm("42501", -551)))
	assert.True(t, repositories.IsPermanentOpenError(perm("42704", -204)))
	assert.False(t, repositories.IsPermanentOpenError(perm("08001", -30081)))
	assert.False(t, repositories.IsPermanentOpenError(errors.New("dial timeout")))
}
