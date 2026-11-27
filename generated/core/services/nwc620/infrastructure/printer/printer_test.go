package printer_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/infrastructure/printer"
	"milton_prism/core/services/nwc620/mocks"
)

func rec(page bool, n int, s string) domain.PrintRecord {
	return domain.PrintRecord{Advance: domain.Advance{Page: page, Lines: n}, Line: s + strings.Repeat(" ", 132-len(s))}
}

func spool() domain.SpoolFile {
	ord, _, _ := domain.PicOrderNo.ParseDec("10248", cobolnum.Truncation)
	return domain.SpoolFile{Name: "NWINVOICE", FormType: "INVOICE", OrderNo: ord,
		Records: []domain.PrintRecord{rec(true, 0, "HD1"), rec(false, 1, "HD2"), rec(false, 2, "HD3"), rec(true, 0, "P2")}}
}

func TestRender_LeadingPageEjectAndExactRecords(t *testing.T) {
	t.Parallel()
	got := string(printer.Render(spool().Records))
	pad := func(s string) string { return s + strings.Repeat(" ", 132-len(s)) }
	// The form feed before the first heading leaves page 1 empty, as IBM i does when the
	// first WRITE is AFTER ADVANCING PAGE; every record keeps its trailing blanks.
	assert.Equal(t, "\f"+pad("HD1")+"\n"+pad("HD2")+"\n\n"+pad("HD3")+"\f"+pad("P2")+"\n", got)
}

func TestRender_NoRecords(t *testing.T) {
	t.Parallel()
	assert.Empty(t, printer.Render(nil))
}

var clock = mocks.FixedClock{T: time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC)}

func TestDirPrinter_WritesAndRenames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := &printer.DirPrinter{Dir: dir, Clock: clock}
	require.NoError(t, p.Print(context.Background(), spool()))
	require.NoError(t, p.Print(context.Background(), spool()))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "two invoices of the same order and second are two files; no temp file is left")
	for _, e := range entries {
		assert.True(t, strings.HasPrefix(e.Name(), "NWINVOICE-INVOICE-10248-20261125T093000-"), e.Name())
		assert.True(t, strings.HasSuffix(e.Name(), ".prt"))
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		assert.Equal(t, printer.Render(spool().Records), b)
	}
}

func TestDirPrinter_NoRecordsNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := &printer.DirPrinter{Dir: dir, Clock: clock}
	f := spool()
	f.Records = nil
	require.NoError(t, p.Print(context.Background(), f))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDirPrinter_CanceledWritesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&printer.DirPrinter{Dir: dir, Clock: clock}).Print(ctx, spool())
	assert.ErrorIs(t, err, context.Canceled)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}

func TestDirPrinter_MissingDir(t *testing.T) {
	t.Parallel()
	err := (&printer.DirPrinter{Dir: filepath.Join(t.TempDir(), "gone"), Clock: clock}).Print(context.Background(), spool())
	assert.Error(t, err)
}

func TestLogPrinter(t *testing.T) {
	t.Parallel()
	require.NoError(t, printer.LogPrinter{}.Print(context.Background(), spool()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, printer.LogPrinter{}.Print(ctx, spool()), context.Canceled)
}

func TestFromEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	p, err := printer.FromEnv(env(map[string]string{printer.EnvSpoolDir: dir, printer.EnvPrintToLog: "true"}), clock)
	require.NoError(t, err)
	assert.IsType(t, &printer.DirPrinter{}, p)

	p, err = printer.FromEnv(env(map[string]string{printer.EnvPrintToLog: "TRUE"}), clock)
	require.NoError(t, err)
	assert.IsType(t, printer.LogPrinter{}, p)

	_, err = printer.FromEnv(env(nil), clock)
	assert.ErrorIs(t, err, printer.ErrNoOutput)
	_, err = printer.FromEnv(env(map[string]string{printer.EnvPrintToLog: "false"}), clock)
	assert.ErrorIs(t, err, printer.ErrNoOutput)
	_, err = printer.FromEnv(env(map[string]string{printer.EnvPrintToLog: "1"}), clock)
	assert.Error(t, err)
	_, err = printer.FromEnv(env(map[string]string{printer.EnvSpoolDir: file}), clock)
	assert.Error(t, err)
	_, err = printer.FromEnv(env(map[string]string{printer.EnvSpoolDir: filepath.Join(dir, "none")}), clock)
	assert.Error(t, err)
}
