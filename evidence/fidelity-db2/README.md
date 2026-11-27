# NWC620 + NWC700 of this build against real Db2 for i (PUB400, 2026-11-27)

This is the real Db2 run that `../fidelity/README.md` left NOT RUN because PUB400 could not be
reached on 2026-11-25. The in-memory runs there tested the report logic. This run replaces the
in-memory repository with each service's own Db2 adapter and reads the rows from real Db2 for i
files, sorted by the host.

## Code under test

- `../signature/deliverable.zip`, sha256
  `7c51d0b491b5d085f93be7665c699da384a9b054ed8fa74946352c1c32b9c6a1`, extracted into a fresh
  scratch directory. All 169 files match the sha256 of `../signature/manifest.json`. No file is
  missing or extra.
- Built offline as its own module with go 1.27.4: `GOFLAGS=-mod=readonly GOPROXY=off
  GOTOOLCHAIN=local CGO_ENABLED=0`. `go build ./...` passed and `go test ./core/services/...`
  passed. `go vet` of the runners is clean.
- The only files added are the two runners, as `cmd/fidelity/db2realrun620/main.go` and
  `cmd/fidelity/db2realrun700/main.go`. They are the runners in `../fidelity/`
  (`db2realrun620.go.txt` and `db2realrun700.go.txt`), the build's runners with labels and paths
  replaced for this public copy, so their sha256 differs from the build's. No file of the deliverable was changed.

## What ran

- Host: PUB400.COM, user PRISMIA, library PRISMIA1 only. Database host server on TLS port 9471.
  CL commands ran through `QSYS2.QCMDEXC`. NWC620 ran at 12:28 UTC and NWC700 at 12:29 UTC.
- DDS: `qddssrc/*.pf` of https://github.com/ZodandSons/northwind-ibmi-cobol-legacy-application
  at `1b12bbb76c7176247abbe58dfd87da40549767c4`, unchanged. The sha256 values match the ones
  recorded in `../fidelity/README.md`: cusmst `790e0e35...`, ordhdr `6382f3d8...`, orddtl
  `f11fe9de...`, prdmst `23ddb2a8...`, codtbl `711302e0...`.
- Tables: `CRTSRCPF PRISMIA1/ZNWDDS RCDLEN(92)`, one member per DDS, then `CRTPF`. Rows: the
  golden `input-*.csv`, loaded with SQL INSERT. NWC620 loaded 6 ORDHDR, 94 ORDDTL, 2 CUSMST,
  4 PRDMST and 3 CODTBL rows. NWC700 loaded CUSMST once per data set: edge 11, empty 0 and
  pages 148 rows.
- Configuration: `db2access.ConfigFromEnv`, with the variables set only inside the runner
  process: `DB2_HOST=pub400.com`, `DB2_PORT=9471`, `DB2_LIBRARY=PRISMIA1`, `DB2_USER=PRISMIA`,
  `DB2_COMMIT_MODE=*NONE` and `DB2_CCSID=273`. The password was read by the runner with
  `os.ReadFile` and never printed.
- CCSID: the server job reported `CCSID 273 | DEFAULT_CCSID 273` (line 2 of each run log). Every
  CHAR column of the created files has CCSID 273 (`QSYS2.SYSCOLUMNS` in the logs).
- Services: NWC620 used `repositories.NewDb2InvoiceRepository` and NWC700 used
  `repositories.NewDb2CustomerRepository`, each over a `svcruntime.Holder` of `db2access.Open`.
  Production printers: `printer.DirPrinter` (NWC620) and `output.SpoolPrinter` (NWC700). Clocks
  were fixed at 2026-11-24 and 2026-11-25 12:00 America/Chicago.
- Comparison rules: the same as `../fidelity/README.md`, unchanged.

## Results

**NWC620: 168 records compared, 168 identical, 0 different** (`nwc620-db2-compare-output.txt`).

| Order | Result | Records COBOL / Go | Differing | Empty first page |
|-------|--------|--------------------|-----------|------------------|
| 1001 | printed | 13 / 13 | 0 | both (R48) |
| 1002 | printed | 12 / 12 | 0 | both |
| 1003 | printed | 63 / 63 | 0 | both |
| 1004 | printed | 57 / 57 | 0 | both |
| 1006 | NW2201 NotFound | no spooled file / none | 0 | n/a (R49 matches) |
| 1007 | printed | 10 / 10 | 0 | both |
| 9999999 | printed | 13 / 13 | 0 | both |

**NWC700: 188 records compared, 186 identical, 2 different** (`nwc700-db2-compare-output.txt`).

| Set | Records COBOL / Go | Differing | Pages COBOL / Go |
|-----|--------------------|-----------|------------------|
| edge | 22 / 22 | 2 | 1 / 1 |
| empty | 3 / 3 | 0 | 1 / 1 |
| pages | 163 / 163 | 0 | 4 / 4 |

The only difference is the one the in-memory run found: the two `TIE CO` customers with equal
sort keys (TESTLAND, TESTVILLE, TIE CO) are swapped.

```
COBOL p1 l26 | C0009  TIE CO ... INSERTED FIRST
COBOL p1 l27 | C0008  TIE CO ... INSERTED SECOND
Go    p1 l26 | C0008  TIE CO ... INSERTED SECOND
Go    p1 l27 | C0009  TIE CO ... INSERTED FIRST
```

The repository's SQL adds `CMCUST` as the last `ORDER BY` key, so the host itself returns C0008
before C0009. The OPM SORT of the golden run kept arrival order. Everything else, including the
host's real EBCDIC sort of the pages set, matches byte for byte on the same page and line. The
results are the same as the in-memory runs: reading packed decimal and CCSID 273 CHAR columns
through `db2access` changed no byte.

## Db2 errors

The services raised no Db2 error: no CCSID mismatch and no SQLSTATE from either repository. The
only failed statement in each log is the cleanup's defensive `DROP ALIAS PRISMIA1.ZZALIAS`
(SQL0204, SQLSTATE 42704). It fails because the alias had already been dropped after each member.

## Cleanup

- NWC620 created ORDHDR, ORDDTL, CUSMST, PRDMST, CODTBL and ZNWDDS (five members), the temporary
  alias ZZALIAS, and five CRTPF listings in 446725/PRISMIA/QPRTJOB (SPLNBR 40 to 44).
- NWC700 created CUSMST and ZNWDDS (one member), ZZALIAS, and one CRTPF listing (SPLNBR 45).
- All were deleted with `DLTF`, `DROP ALIAS` and `DLTSPLF`.
- Before and after each run, `OBJECT_STATISTICS('PRISMIA1','*ALL')` lists only QCBLLESRC, QCLSRC,
  QCMDSRC, QCPPSRC, QDDSSRC and QRPGLESRC (*FILE PF). `SYSPARTITIONSTAT` shows no member in
  PRISMIA1, and `OUTPUT_QUEUE_ENTRIES_BASIC` shows no spooled file of PRISMIA.
- Both runners print `objects true, members true, spooled files true (same as before), every
  listing query answered true`.
- Nothing was created in QGPL.

## Files

| File | Content |
|------|---------|
| `nwc620-run-log.txt` | full NWC620 runner output (ANSI colour codes stripped) |
| `nwc620-db2-compare-output.txt` | NWC620 comparison section of that log |
| `nwc700-run-log.txt` | full NWC700 runner output (ANSI colour codes stripped) |
| `nwc700-db2-compare-output.txt` | NWC700 comparison section of that log |

Nothing here runs in CI.
