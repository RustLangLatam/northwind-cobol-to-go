package svcruntime

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// A generated service listens on two ports: gRPC and the probes and metrics. The source
// gate and the dependency gate look for anything else before the service runs; this
// looks at the running process, which is the only place a listener started by a
// dependency's init() or by a handler is certain to show (C51, ADR-0070).

// What this check is, and is not (C65, C71, C77; ADR-0070). It is a second line behind the
// import gate, the dependency gate and the platform's default-deny egress policy:
//
//   - Linux only. Elsewhere it checks nothing, because there is no portable view of a
//     process's own sockets; the source and dependency gates still apply.
//   - It runs once after the listeners are up and then every listenerRecheck (30 s). A
//     listener opened and closed between two checks goes unseen, and so does one a dependency
//     opens from init() and closes before the first check. Nothing here replaces the import
//     gate and the egress policy.
//   - Reading the process's file descriptors and reading the /proc/net tables are two reads,
//     not one: a socket opened between them is missed until the next check.
//   - The runtime's own sockets are the two TCP listeners it opens from host:port, so a
//     non-TCP gRPC address does not exist: ownPort registers TCP addresses only, and any
//     other socket is reported, never excused.
//   - Judged by protocol and port: a UDP socket on the gRPC port is not the gRPC listener.
//     Any raw, icmp or packet socket, and any listening or bound unix socket, is refused.

// selfProc is the process's own /proc directory (a variable so a test can point it at
// a directory that cannot be read).
var selfProc = "/proc/self"

// listenerRecheck is how often Run looks again for a listener that appeared after it
// started (a variable so tests can shorten it).
var listenerRecheck = 30 * time.Second

// listenerStopTimeout is how long Run waits, at shutdown, for the listener watcher to leave a
// check that is still reading /proc (a variable so a test can shorten it). A check that
// does not return in time is abandoned: Fail does not block and ownedPorts is a sync.Map,
// so it can do no harm to a server that has stopped (C88).
var listenerStopTimeout = 5 * time.Second

// sockKey is a socket the runtime owns: the protocol family ("tcp" covers tcp and tcp6,
// "udp" covers udp and udp6) and the port. A UDP socket on the gRPC port is not the
// gRPC listener (C68).
type sockKey struct {
	proto string
	port  int
}

// ownedPorts holds the sockets every running Server of this process listens on. A
// process has one Server; the set exists so tests that run two do not trip each other.
var ownedPorts sync.Map

// ownPort registers a TCP listener's address. A non-TCP address is not registered: the
// runtime listens on host:port over TCP only (C77), so anything else is not its own.
func ownPort(addr net.Addr) (key sockKey, ok bool) {
	t, ok := addr.(*net.TCPAddr)
	if !ok {
		return sockKey{}, false
	}
	key = sockKey{"tcp", t.Port}
	ownedPorts.Store(key, struct{}{})
	return key, true
}

func ownedPortSet() map[sockKey]bool {
	set := map[sockKey]bool{}
	ownedPorts.Range(func(k, _ any) bool { set[k.(sockKey)] = true; return true })
	return set
}

// listenerCheck is checkListeners (a variable so a test can slow it down).
var listenerCheck = checkListeners

// checkListeners fails when this process holds a socket no Server owns. On Linux it
// reads /proc and fails closed when it cannot; elsewhere it checks nothing (best
// effort: there is no portable view of a process's sockets).
func checkListeners(proc string) error {
	got, err := unexpectedListeners(proc, ownedPortSet())
	if err != nil {
		return fmt.Errorf("svcruntime: cannot verify the listening sockets (the check fails closed on Linux): %w", err)
	}
	if len(got) > 0 {
		return fmt.Errorf("svcruntime: this process listens on %s, which the runtime does not own; only its gRPC and metrics TCP ports may listen (ADR-0070)", strings.Join(got, ", "))
	}
	return nil
}
