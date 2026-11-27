//go:build !linux

package svcruntime

// unexpectedListeners checks nothing off Linux: there is no portable view of a process's
// own sockets, and the source and dependency gates still apply. The check fails closed
// only on Linux (ADR-0070).
func unexpectedListeners(string, map[sockKey]bool) ([]string, error) { return nil, nil }
