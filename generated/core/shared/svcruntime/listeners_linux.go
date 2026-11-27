//go:build linux

package svcruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// How a /proc/net table is judged.
const (
	kindInet   = iota // tcp, udp: a socket is a listener in one state; its port must be owned
	kindAny           // raw, icmp: the service owns no such socket at all
	kindUnix          // unix: a listening or bound socket is refused; a connected client is not
	kindPacket        // packet: the service owns no such socket at all
)

// socketTables are the /proc/net tables read, each with how it is judged. For kindInet,
// listenState is the state that means "listening": 0A is TCP LISTEN; 07 is an unconnected
// UDP socket (a connected one, a resolver's, is 01). inode is the column of the inode.
var socketTables = []struct {
	name        string
	kind        int
	proto       string // for kindInet: the family the port is owned under
	listenState string
	inode       int
}{
	{"tcp", kindInet, "tcp", "0A", 9}, {"tcp6", kindInet, "tcp", "0A", 9},
	{"udp", kindInet, "udp", "07", 9}, {"udp6", kindInet, "udp", "07", 9},
	{"raw", kindAny, "", "", 9}, {"raw6", kindAny, "", "", 9},
	{"icmp", kindAny, "", "", 9}, {"icmp6", kindAny, "", "", 9},
	{"unix", kindUnix, "", "", 6},
	{"packet", kindPacket, "", "", 8},
}

// soAcceptcon is the unix socket flag of a socket in the listening state.
const soAcceptcon = 0x10000

// unexpectedListeners returns a description of every socket THIS process owns that the
// runtime does not: a tcp or udp listener on a protocol and port outside allowed, any raw,
// icmp or packet socket, and a listening or bound unix socket. proc is /proc/self (or a
// fixture): its fd directory names the process's sockets by inode, and its net tables
// describe them. A table that does not exist (no IPv6) counts as empty, except that tcp or
// tcp6 must exist (C89); anything else unreadable or unparsable is an error, so the caller
// fails closed.
func unexpectedListeners(proc string, allowed map[sockKey]bool) ([]string, error) {
	inodes, err := ownSocketInodes(filepath.Join(proc, "fd"))
	if err != nil {
		return nil, err
	}
	var out []string
	sawTCP := false
	for _, tb := range socketTables {
		raw, err := os.ReadFile(filepath.Join(proc, "net", tb.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		sawTCP = sawTCP || tb.name == "tcp" || tb.name == "tcp6"
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(raw), "\n")
		for _, line := range lines[1:] { // the first line is the header
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) <= tb.inode {
				return nil, fmt.Errorf("/proc/net/%s: unreadable row %q", tb.name, line)
			}
			if !inodes[fields[tb.inode]] {
				continue
			}
			desc, err := judgeSocket(tb.name, tb.kind, tb.proto, tb.listenState, fields, allowed)
			if err != nil {
				return nil, fmt.Errorf("/proc/net/%s: %w", tb.name, err)
			}
			if desc != "" {
				out = append(out, desc)
			}
		}
	}
	if !sawTCP { // a /proc/net with no tcp table at all reads nothing: the check cannot see the listeners (C89)
		return nil, fmt.Errorf("/proc/net has neither a tcp nor a tcp6 table")
	}
	sort.Strings(out)
	return out, nil
}

// judgeSocket describes a socket of the process the runtime does not own, or returns "".
func judgeSocket(table string, kind int, proto, listenState string, fields []string, allowed map[sockKey]bool) (string, error) {
	switch kind {
	case kindInet:
		if fields[3] != listenState {
			return "", nil
		}
		port, err := portOf(fields[1])
		if err != nil {
			return "", err
		}
		if allowed[sockKey{proto, port}] {
			return "", nil
		}
		return fmt.Sprintf("%s :%d", table, port), nil
	case kindUnix:
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			return "", fmt.Errorf("flags %q: %w", fields[3], err)
		}
		path := "(unnamed)"
		if len(fields) > 7 {
			path = fields[7]
		}
		if flags&soAcceptcon != 0 || (fields[5] == "01" && len(fields) > 7) { // listening, or bound and unconnected
			return "unix " + path, nil
		}
		return "", nil
	default:
		return table + " socket", nil
	}
}

// readlink is os.Readlink (a variable so a test can make it fail).
var readlink = os.Readlink

// ownSocketInodes returns the inodes of the sockets among the process's open files. A
// descriptor that closed since the listing (ErrNotExist) is skipped; any other error
// means the process cannot be seen, and is returned (C75).
func ownSocketInodes(fdDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, err
	}
	inodes := map[string]bool{}
	for _, e := range entries {
		target, err := readlink(filepath.Join(fdDir, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ino, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(ino, "]")] = true
		}
	}
	return inodes, nil
}

// portOf reads the port of a "HEXADDR:HEXPORT" local address.
func portOf(local string) (int, error) {
	i := strings.LastIndexByte(local, ':')
	if i < 0 {
		return 0, fmt.Errorf("local address %q has no port", local)
	}
	p, err := strconv.ParseUint(local[i+1:], 16, 16)
	if err != nil {
		return 0, fmt.Errorf("local address %q: %w", local, err)
	}
	return int(p), nil
}
