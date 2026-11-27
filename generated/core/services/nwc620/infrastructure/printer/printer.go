// Package printer writes the spooled file of an invoice. NWC620C overrides QSYSPRT
// (PAGESIZE(66 132), FORMTYPE(INVOICE), SPLFNAME(NWINVOICE), HOLD(*YES)); the port
// writes the spooled file as a text document in an output directory, or, when the
// operator opts in, to the debug log.
package printer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/ports"
	applog "milton_prism/pkg/log"
)

// Render lays out the records as the printed pages: a form feed for AFTER ADVANCING
// PAGE, n line feeds before a record written AFTER ADVANCING n LINES, every record its
// full 132 bytes (trailing blanks kept), and a final line feed.
//
// The first WRITE of NWC620 is AFTER ADVANCING PAGE, which on IBM i ejects an empty
// page 1 before the first heading. The form feed that starts the document reproduces
// that empty page.
//
// PORT-GAP: [The printer file attributes of the override are not carried by a text file]
// OVRPRTF also sets LPI(6), CPI(10), HOLD(*YES) and the output queue; a file in a
// directory has none of them. FORMTYPE and SPLFNAME are kept in the file name.
func Render(records []domain.PrintRecord) []byte {
	var b strings.Builder
	for _, r := range records {
		if r.Advance.Page {
			b.WriteByte('\f')
		} else {
			b.WriteString(strings.Repeat("\n", r.Advance.Lines))
		}
		b.WriteString(r.Line)
	}
	if len(records) > 0 {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

var _ ports.InvoicePrinter = (*DirPrinter)(nil)

// DirPrinter writes each spooled file to Dir.
type DirPrinter struct {
	Dir   string
	Clock ports.Clock
}

// Print writes the document to a temporary file in the directory and renames it into
// place, so a reader never sees half an invoice.
func (p *DirPrinter) Print(ctx context.Context, f domain.SpoolFile) error {
	if len(f.Records) == 0 { // an opened and closed printer file with no WRITE spools nothing
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("nwc620: spool: %w", err)
	}
	tmp, err := os.CreateTemp(p.Dir, "."+f.Name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("nwc620: spool: create: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(Render(f.Records)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("nwc620: spool: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("nwc620: spool: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("nwc620: spool: close: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("nwc620: spool: %w", err)
	}
	// The random part of the temporary name keeps two invoices of the same order and
	// second apart, as the spooled file number does on IBM i.
	unique := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tmp.Name()), "."+f.Name+"-"), ".tmp")
	final := filepath.Join(p.Dir, fmt.Sprintf("%s-%s-%s-%s-%s.prt", f.Name, f.FormType,
		strings.TrimSpace(f.OrderNo.Plain()), p.Clock.Now().Format("20060102T150405"), unique))
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("nwc620: spool: rename: %w", err)
	}
	keep = true
	return nil
}

var _ ports.InvoicePrinter = LogPrinter{}

// LogPrinter writes every record to the debug log, each value quoted. It is opt-in
// (NWC620_PRINT_TO_LOG=true): an invoice carries names, addresses and amounts.
type LogPrinter struct{}

// Print logs the spooled file at Debug.
func (LogPrinter) Print(ctx context.Context, f domain.SpoolFile) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("nwc620: print to log: %w", err)
	}
	for i, r := range f.Records {
		adv := "page"
		if !r.Advance.Page {
			adv = strconv.Itoa(r.Advance.Lines)
		}
		applog.Debugf("nwc620: spool=%s formtype=%s record=%d advance=%s line=%s",
			strconv.Quote(f.Name), strconv.Quote(f.FormType), i+1, adv, strconv.Quote(r.Line))
	}
	return nil
}

// Environment variables of the output target.
const (
	EnvSpoolDir   = "NWC620_SPOOL_DIR"    // directory the invoices are written to
	EnvPrintToLog = "NWC620_PRINT_TO_LOG" // "true": log each record at Debug instead
)

// ErrNoOutput: neither output target is configured.
var ErrNoOutput = errors.New("nwc620: set " + EnvSpoolDir + " to an existing directory, or " + EnvPrintToLog + "=true")

// FromEnv picks the output target: the spool directory when it is set, else the log
// when the operator opted in; neither is an error.
func FromEnv(getenv func(string) string, clock ports.Clock) (ports.InvoicePrinter, error) {
	if dir := strings.TrimSpace(getenv(EnvSpoolDir)); dir != "" {
		st, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("nwc620: %s: %w", EnvSpoolDir, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("nwc620: %s is not a directory", EnvSpoolDir)
		}
		return &DirPrinter{Dir: dir, Clock: clock}, nil
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvPrintToLog))) {
	case "true":
		return LogPrinter{}, nil
	case "", "false":
		return nil, ErrNoOutput
	default:
		return nil, fmt.Errorf("nwc620: %s must be true or false", EnvPrintToLog)
	}
}
