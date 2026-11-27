package svcruntime

import (
	"os"
	"syscall"
)

// terminationSignals are the signals that stop Run. os has no SIGTERM, so this is
// the one use of syscall in the skeleton, kept in its own file.
var terminationSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
