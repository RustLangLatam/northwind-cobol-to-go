package output_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/infrastructure/output"
	"milton_prism/core/shared/domainerr"
)

func sampleReport() domain.Report {
	return domain.Report{
		RunTime: time.Date(2026, 11, 25, 9, 30, 5, 0, time.UTC),
		Lines: []domain.PrintLine{
			{NewPage: true, Text: "HD1   "},
			{Advance: 2, Text: "HD2"},
			{Advance: 1, Text: "DETAIL  "},
			{NewPage: true, Text: "HD1 P2"},
			{Advance: 3, Text: "TOTAL"},
		},
		Customers: 1, Pages: 2,
	}
}

func TestRender_CarriageControl(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "HD1\n\nHD2\nDETAIL\n\fHD1 P2\n\n\nTOTAL\n", string(output.Render(sampleReport())))
	assert.Empty(t, output.Render(domain.Report{}))
}

func TestSpoolPrinter_WritesOneFilePerRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := output.NewSpoolPrinter(dir)
	first, err := p.Print(context.Background(), sampleReport())
	require.NoError(t, err)
	second, err := p.Print(context.Background(), sampleReport())
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.True(t, strings.HasPrefix(filepath.Base(first), "NWC700-QSYSPRT-20261125-093005-"))
	body, err := os.ReadFile(first)
	require.NoError(t, err)
	assert.Equal(t, output.Render(sampleReport()), body)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temporary file is left behind")
}

func TestSpoolPrinter_CanceledWritesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := output.NewSpoolPrinter(dir).Print(ctx, sampleReport())
	assert.ErrorIs(t, err, context.Canceled)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestSpoolPrinter_MissingDirectory(t *testing.T) {
	t.Parallel()
	_, err := output.NewSpoolPrinter(filepath.Join(t.TempDir(), "gone")).Print(context.Background(), sampleReport())
	assert.True(t, domainerr.Is(err, domainerr.Unavailable))
}

func TestLogPrinter(t *testing.T) {
	t.Parallel()
	out, err := output.LogPrinter{}.Print(context.Background(), sampleReport())
	require.NoError(t, err)
	assert.Equal(t, "log", out)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = output.LogPrinter{}.Print(ctx, sampleReport())
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPrinterFromEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	p, err := output.PrinterFromEnv(env(map[string]string{output.EnvSpoolDir: dir, output.EnvPrintToLog: "true"}))
	require.NoError(t, err)
	assert.IsType(t, &output.SpoolPrinter{}, p)

	p, err = output.PrinterFromEnv(env(map[string]string{output.EnvPrintToLog: "true"}))
	require.NoError(t, err)
	assert.IsType(t, output.LogPrinter{}, p)

	_, err = output.PrinterFromEnv(env(nil))
	assert.ErrorIs(t, err, output.ErrNoOutput)
	_, err = output.PrinterFromEnv(env(map[string]string{output.EnvPrintToLog: "false"}))
	assert.ErrorIs(t, err, output.ErrNoOutput)
	_, err = output.PrinterFromEnv(env(map[string]string{output.EnvPrintToLog: "maybe"}))
	assert.Error(t, err)
	_, err = output.PrinterFromEnv(env(map[string]string{output.EnvSpoolDir: file}))
	assert.Error(t, err)
	_, err = output.PrinterFromEnv(env(map[string]string{output.EnvSpoolDir: filepath.Join(dir, "missing")}))
	assert.Error(t, err)
}
