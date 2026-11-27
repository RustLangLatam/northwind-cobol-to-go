// Package i400conn links the hardened i400 fork (github.com/RustLangLatam/i400) into
// pkg/db2access/ibmi. A composition root imports it for its side effect:
//
//	import _ "milton_prism/pkg/db2access/ibmi/i400conn"
//
// It is the only package in the platform that imports the fork. The connector it
// builds is made from ibmi.Params, whose TLS field is cfg.TLSConfig(): the verified
// configuration (CA pool and server name; the fork has no way to skip verification).
// The password goes from Params into i400.Config.Password and nowhere else; no DSN
// string is built, so there is nothing to leak through an error.
//
// VERSION. The fork is pinned in go.mod and in infra/generation-agent/prewarm-go.mod
// (ADR-0056 task 3.6). To move to a new tag: `go get github.com/RustLangLatam/i400@<tag>`,
// set the same version in prewarm-go.mod, and rebuild the generation-agent image
// (platform.sh does not rebuild it).
package i400conn

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"

	"github.com/RustLangLatam/i400"

	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/ibmi"
)

func init() { ibmi.RegisterConnect(Connect) }

// commitmentControl maps the commit mode to the fork's commitment-control level
// (0 none, 1 *CS, 2 *CHG, 3 *ALL, 4 *RR; the same levels the fork's BeginTx uses).
func commitmentControl(m db2access.CommitMode) (int, error) {
	switch m {
	case db2access.CommitNone:
		return 0, nil
	case db2access.CommitCS:
		return 1, nil
	case db2access.CommitChg:
		return 2, nil
	case db2access.CommitAll:
		return 3, nil
	case db2access.CommitRR:
		return 4, nil
	}
	return 0, fmt.Errorf("i400conn: unknown commit mode %q", m)
}

// Config builds the fork's configuration for p. It is exported for the tests.
func Config(p ibmi.Params) (i400.Config, error) {
	// p.CCSID is the job CCSID db2access.Config declares for its text guard; the fork
	// translates text by each column's own CCSID and has no override, so nothing of it
	// goes into the connection.
	level, err := commitmentControl(p.CommitMode)
	if err != nil {
		return i400.Config{}, err
	}
	scheme := "as400s"
	if p.TLS == nil {
		scheme = "as400" // AllowPlaintext only: ibmi.ParamsFor yields no TLS-less Params otherwise
	}
	// The DSN carries no credential: host, schema and options only. ParseDSN gives the
	// fork's defaults for everything else.
	dsn := url.URL{Scheme: scheme, Host: p.Host, Path: "/" + p.Library, RawQuery: url.Values{
		"port": {fmt.Sprint(p.Port)}, "libraries": {p.Library},
		// SQL naming: an unqualified table resolves in the current schema only, which is
		// what the journaling check in package ibmi assumes.
		"naming": {"sql"},
		// ISO date (yyyy-mm-dd) and time (hh.mm.ss) for the connection, whatever the user
		// profile's job DATFMT/TIMFMT (*MDY, *DMY, *YMD, *JUL). db2access does not rely on
		// it (its SQL names ISO in every statement, ADR-0064); this is defence in depth for
		// any text the host converts with the connection's format.
		"dateFormat": {"iso"}, "timeFormat": {"iso"},
		// A context deadline (db2access.Config.QueryTimeout) becomes the host server's native
		// cancel, which stops the statement on the host. Pinned here, not left to the fork's
		// default; qqrytimlmt only rejects by estimate and never stops a running query.
		"queryTimeoutMechanism": {"cancel"},
	}.Encode()}
	cfg, err := i400.ParseDSN(dsn.String())
	if err != nil {
		return i400.Config{}, errors.New("i400conn: the connection settings are not valid for the i400 driver")
	}
	cfg.User = p.User
	cfg.Password = p.Password.Reveal()
	cfg.CommitmentControl = level
	cfg.MinPasswordLevel = p.MinPasswordLevel // refuses a server-driven downgrade to DES before any password is sent
	cfg.MaxPacketBytes = 16 << 20             // fetch blocks are at most 16000 KB; no CAST text needs more
	if p.TLS != nil {
		cfg.UseTLS = true
		cfg.TLSRootCAs = p.TLS.RootCAs
		cfg.TLSServerName = p.TLS.ServerName
	}
	return *cfg, nil
}

// Connect is the ibmi.ConnectFunc of the i400 driver.
func Connect(p ibmi.Params) (driver.Connector, error) {
	cfg, err := Config(p)
	if err != nil {
		return nil, err
	}
	return i400.NewConnector(cfg)
}
