// Package output writes the NWC700 report where the source wrote QSYSPRT: a spool
// directory, or the debug log when the operator asks for it.
package output

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/ports"
	applog "milton_prism/pkg/log"
)

// Environment variables of the report output.
const (
	EnvSpoolDir   = "NWC700_SPOOL_DIR"    // directory that receives one file per run
	EnvPrintToLog = "NWC700_PRINT_TO_LOG" // "true": write the report lines to the debug log
)

// ErrNoOutput is returned when no output target is configured.
var ErrNoOutput = errors.New("nwc700: set " + EnvSpoolDir + " to a directory (or " + EnvPrintToLog + "=true) to receive the customer list")

// PrinterFromEnv selects the report output: the spool directory wins over the log.
func PrinterFromEnv(getenv func(string) string) (ports.ReportPrinter, error) {
	if dir := strings.TrimSpace(getenv(EnvSpoolDir)); dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("nwc700: %s: %w", EnvSpoolDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("nwc700: %s is not a directory", EnvSpoolDir)
		}
		return NewSpoolPrinter(dir), nil
	}
	if v := strings.TrimSpace(getenv(EnvPrintToLog)); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("nwc700: %s must be true or false: %w", EnvPrintToLog, err)
		}
		if on {
			return LogPrinter{}, nil
		}
	}
	return nil, ErrNoOutput
}

// Render turns the print lines into text with the carriage control applied: a form
// feed for AFTER ADVANCING PAGE (none before the first page), n line ends for
// AFTER ADVANCING n LINES.
//
// PORT-NOTE: [The spool file becomes a text file with form feeds and blank lines] QSYSPRT records of 132 columns carry their carriage control; the port writes each record with trailing blanks removed, a form feed for a new page and blank lines for the extra lines advanced, which prints the same page.
func Render(report domain.Report) []byte {
	var b strings.Builder
	for i, l := range report.Lines {
		switch {
		case l.NewPage && i > 0:
			b.WriteString("\n\f")
		case !l.NewPage && i > 0:
			b.WriteString(strings.Repeat("\n", l.Advance))
		case !l.NewPage:
			b.WriteString(strings.Repeat("\n", l.Advance-1))
		}
		b.WriteString(strings.TrimRight(l.Text, " "))
	}
	if len(report.Lines) > 0 {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// SpoolPrinter writes each report to its own file in a directory.
type SpoolPrinter struct{ dir string }

// NewSpoolPrinter writes into dir.
func NewSpoolPrinter(dir string) *SpoolPrinter { return &SpoolPrinter{dir: dir} }

// Print writes the report to a temporary file in the directory and renames it into
// place, so a reader never sees half a report.
func (p *SpoolPrinter) Print(ctx context.Context, report domain.Report) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("nwc700: spool: %w", err)
	}
	tmp, err := os.CreateTemp(p.dir, ".nwc700-*.tmp")
	if err != nil {
		return "", domain.PrintFailed(err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(Render(report)); err != nil {
		_ = tmp.Close()
		return "", domain.PrintFailed(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", domain.PrintFailed(err)
	}
	if err := tmp.Close(); err != nil {
		return "", domain.PrintFailed(err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("nwc700: spool: %w", err)
	}
	// The random part of the temporary name keeps two runs in the same second apart.
	unique := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tmpName), ".nwc700-"), ".tmp")
	final := filepath.Join(p.dir, fmt.Sprintf("NWC700-QSYSPRT-%s-%s.txt", report.RunTime.Format("20060102-150405"), unique))
	if err := os.Rename(tmpName, final); err != nil {
		return "", domain.PrintFailed(err)
	}
	done = true
	return final, nil
}

// LogPrinter writes the report lines to the debug log, each line quoted.
type LogPrinter struct{}

// Print logs every rendered line at Debug.
func (LogPrinter) Print(ctx context.Context, report domain.Report) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("nwc700: log printer: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(Render(report)), "\n"), "\n") {
		applog.Debugf("nwc700: QSYSPRT line=%s", strconv.Quote(line))
	}
	return "log", nil
}
