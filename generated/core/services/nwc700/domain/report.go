package domain

import "time"

// PrintLine is one record written to QSYSPRT with its carriage control: NewPage is
// AFTER ADVANCING PAGE, otherwise the printer advances Advance lines first.
type PrintLine struct {
	NewPage bool
	Advance int
	Text    string
}

// Report is one run of the customer list: the spooled lines plus the counts the
// program kept.
type Report struct {
	RunTime   time.Time // the business clock when the run started (ACCEPT FROM DATE)
	Lines     []PrintLine
	Customers int // WS-TOTAL-CNT
	Pages     int // WS-PAGE, not wrapped at 999
}
