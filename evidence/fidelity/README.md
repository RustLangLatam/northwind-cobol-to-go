# NWC620 + NWC700 fidelity on the attested deliverable of this two-service build (2026-11-25)

Status: **in-memory runs done; real Db2 for i runs done** on 2026-11-27 (see the last section and [`../fidelity-db2/`](../fidelity-db2/)).

## Code under test

- `evidence/signature/deliverable.zip`, sha256
  `7c51d0b491b5d085f93be7665c699da384a9b054ed8fa74946352c1c32b9c6a1` (the attestation's
  signed subject), extracted into a fresh scratch directory. All 169 files match the sha256 of
  `evidence/signature/manifest.json`; no file is missing or extra.
- Built as its own module (`milton_prism`, go 1.27.4) offline: `GOFLAGS=-mod=readonly
  GOPROXY=off GOTOOLCHAIN=local`, local module cache. `go build ./...` and
  `go test ./core/services/...` passed.
- The only files added are the four runners under `cmd/fidelity/`. No file of the
  deliverable was changed.

## Runners

| File | Service | Repository |
|------|---------|------------|
| `goldencompare620.go.txt` | NWC620 | in memory, from the golden CSVs |
| `db2realrun620.go.txt` | NWC620 | `repositories.NewDb2InvoiceRepository` on Db2 for i (PUB400); ran 2026-11-27 |
| `goldencompare700.go.txt` | NWC700 | in memory, from the golden CSVs |
| `db2realrun700.go.txt` | NWC700 | `repositories.NewDb2CustomerRepository` on Db2 for i (PUB400); ran 2026-11-27 |

### NWC620: changes from the earlier one-service build's runners

The NWC620 API of this build differs from the earlier one-service build. The comparison rules did not change.

- Repository port: `GetOrderHeader`, `GetCustomer`, `GetCodeDescription`, `ListOrderLines` and
  `GetProductName` (returns the name). Domain fields renamed (`OrderNo`, `Customer`,
  `ShipAddress`, `LineNo`, `ProductNo`, `Extended`, ...) and pictures renamed (`PicOrderNo`,
  `PicDate7`, `PicLineNo`, `PicProductNo`). `domain.Customer` has no key field.
- The in-memory repository pads every CHAR value to its DDS width
  (`repositories/tables.go`), as Db2 returns CHAR. The earlier build used the unpadded CSV values.
- `PrintInvoice(ctx, int64)` instead of a `cobolnum.Dec`.
- `application.NewService(repo, printer, clock, metrics)`: a no-op `ports.Metrics` is passed;
  the clock is `svcruntime.NewClock` (fixed 2026-11-24 12:00 America/Chicago, as in the earlier build).
- Printer: `printer.DirPrinter{Dir, Clock}`, the production writer (earlier build: `spool.NewDirWriter`).
  Record length constant: `domain.PrintRecordLength` (earlier build: `domain.RecordLength`).
- Order not on file: this build returns no status but the error `NW2201 Failure_Order_Not_Found`
  (`domainerr.NotFound`). R49 passes when that error comes back AND the spool directory is empty.
- Permanent open errors: the `repositories` package of this build has no `IsPermanentOpenError`; the
  Db2 runner copies `isPermanent` from `core/cmd/nwc620-services/main.go`.
- Both runners also count identical records (the earlier build printed only compared and differing).
- The Db2 runners fail the cleanup verification when any listing query fails. Without this,
  a run that could not connect printed `objects true, members true, spooled files true`
  because both empty listings matched.

### NWC700: rules

Golden: `../golden-nwc700/`. Data sets edge, empty and pages; clock fixed at
2026-11-25 12:00 America/Chicago (the golden headings print `11/25/26`).

- Every record is compared on its 133 bytes (FCFC control plus 132 bytes) and on its page
  and line, as `p<page> l<line> |<control><132 bytes>|`.
- Golden side: the PUB400 footer `-=* https://pub400.com *=-` and the blank spacing lines are
  removed. The empty first page is removed too, because the golden README says it is IBM i
  behaviour and not NWC700 output. Golden pages are then numbered from the first page
  NWC700 wrote (golden page n becomes n-1).
- Go side: the file written by the production `output.SpoolPrinter` (`output.Render`). The
  first record opens page 1 with control `1`; `"\n\f"` starts a new page; n line ends mean
  control ` `, `0` or `-`; the closing line end is not a record. `Render` strips trailing
  blanks (its PORT-NOTE), so each record is padded back to 132 bytes with blanks. A record
  longer than 132 bytes is not cut, so it shows up as a difference. The NWC700 port writes
  no empty first page. That is not counted as a difference, because the golden README
  says the host's empty page is not program output.
- `Result.Pages` is also compared with the golden page count after stripping.
- In memory, the repository mirrors the Db2 repository's `ListByCountry`: CHAR values are
  blank padded to their widths and sorted by `ORDER BY CMCNTY, CMCITY, CMNAME, CMCUST`, in
  EBCDIC byte order (`charmap.CodePage037`). Every character of the data sets is the same in
  CCSID 37 and 273. So the in-memory run tests the report logic. The Db2 run tests the
  host's real sort: it gave the same two-record swap (see the last section).

## Results: in memory

**NWC620: 168 records compared, 168 identical, 0 different** (`nwc620-golden-compare-output.txt`).

| Order | Result | Records COBOL / Go | Differing | Empty first page |
|-------|--------|--------------------|-----------|------------------|
| 1001 | printed | 13 / 13 | 0 | both (R48) |
| 1002 | printed | 12 / 12 | 0 | both |
| 1003 | printed | 63 / 63 | 0 | both |
| 1004 | printed | 57 / 57 | 0 | both |
| 1006 | NW2201 NotFound | no spooled file / none | 0 | none (R49 matches) |
| 1007 | printed | 10 / 10 | 0 | both |
| 9999999 | printed | 13 / 13 | 0 | both |

**NWC700: 188 records compared, 186 identical, 2 different** (`nwc700-golden-compare-output.txt`).

| Set | Records COBOL / Go | Differing | Pages COBOL / Go |
|-----|--------------------|-----------|------------------|
| edge | 22 / 22 | 2 | 1 / 1 |
| empty | 3 / 3 | 0 | 1 / 1 |
| pages | 163 / 163 | 0 | 4 / 4 |

The difference: the two `TIE CO` customers have equal keys (TESTLAND, TESTVILLE, TIE CO).

```
COBOL p1 l26 | C0009  TIE CO ... INSERTED FIRST
COBOL p1 l27 | C0008  TIE CO ... INSERTED SECOND
Go    p1 l26 | C0008  TIE CO ... INSERTED SECOND
Go    p1 l27 | C0009  TIE CO ... INSERTED FIRST
```

The OPM SORT kept arrival order for equal keys in the golden run. The real Db2 run gave the same swap, so the host's sort does not change the result. The port adds `CMCUST` as
a last sort key (PORT-NOTE in `db2_customer_repository.go`: "The COBOL SORT leaves equal keys
in an undefined order"). It sorts C0008 before C0009, so these two records are swapped. The upstream SORT has no `WITH DUPLICATES IN ORDER` clause, so COBOL does not define the relative order of records with equal keys. The host printed them in arrival order, the Go service by customer number; the source program guarantees neither. The statement is at [qcblsrc/nwc700.cbl, lines 115-116](https://github.com/ZodandSons/northwind-ibmi-cobol-legacy-application/blob/1b12bbb76c7176247abbe58dfd87da40549767c4/qcblsrc/nwc700.cbl#L115-L116).
Everything else matches byte for byte: the blank country, the EBCDIC order of lower case
and digits, status D printed, full-width fields, every page break of the pages set, and the
empty file.

## Results: real Db2 for i (PUB400): done on 2026-11-27

Both Db2 runners ran against PUB400 on 2026-11-27 through each service's own Db2 adapter
(`db2access.ConfigFromEnv`, job CCSID 273, commit mode `*NONE`, TLS). The full logs and the
README of that run are in [`../fidelity-db2/`](../fidelity-db2/).

| Service | Records compared | Identical | Differing | Db2 errors |
|---------|------------------|-----------|-----------|------------|
| NWC620 | 168 | 168 | 0 | none |
| NWC700 | 188 | 186 | 2 | none |

- **NWC620.** The six printed orders match, with the empty first page on both sides. Order
  1006 is not on file: the service returns `NW2201`, no spooled file is made, and R49 matches.
- **NWC700.** The empty and pages sets match. The edge set differs only by the two equal-key
  `TIE CO` customers, swapped as described above.
- **Tie order.** The repository's SQL adds `CMCUST` as the last `ORDER BY` key, so the host
  returns C0008 before C0009. The COBOL SORT of the golden run kept arrival order.
- **Setup and cleanup.** The runners loaded the golden CSV rows into new files in PRISMIA1 with
  SQL `INSERT`, ran the services, and deleted the files and the spooled listings. The library
  was checked before and after: the object, member and spooled file listings match. The only
  failed statement is the cleanup's defensive `DROP ALIAS` (SQL0204), which fails because the
  alias had already been dropped.

An earlier attempt on 2026-11-25 failed at its first connection, before any statement reached
the host. That log was discarded.

To repeat the run:
`db2realrun620 run <ddsdir> ../golden-nwc620 <spooldir>`, then
`db2realrun700 run <ddsdir> ../golden-nwc700 <spooldir>`. Settings:
DB2_CCSID=273, DB2_COMMIT_MODE=*NONE, library PRISMIA1 only, with the before/after listings
of objects, members and spooled files.

Nothing here runs in CI.
