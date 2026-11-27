# PUB400: NWC620 golden invoices from a real ILE COBOL run (2026-11-24)

Task R46 of `docs/openspec/changes/nwc620-review-remediation/tasks.md`. The decimal golden tests of
the Go port derived their expected values by hand. These files are the expected output of the
original program, compiled and run by ILE COBOL on a real IBM i.

## Host and source

- Host: PUB400.COM, IBM i 7.5, user PRISMIA. Only library PRISMIA1 was used. The connection used
  the i400 driver over the database host server (TLS 9471). CL commands ran through `QSYS2.QCMDEXC`.
  SSH and 5250 were not used.
- Source: https://github.com/ZodandSons/northwind-ibmi-cobol-legacy-application at commit
  `1b12bbb76c7176247abbe58dfd87da40549767c4`: `qcbllesrc/nwc620.cblle`, the copybooks ORDHDRCP,
  ORDDTLCP, CUSMSTCP, PRDMSTCP and CODTBLCP, and the DDS `qddssrc/{ordhdr,orddtl,cusmst,prdmst,codtbl}.pf`.
  The sources were uploaded unchanged. The DDS has no field reference file.
- Run: 2026-11-24, 11:11 to 11:12 host time. The job date the program used (ACCEPT FROM DATE) is
  printed in every heading as `DATE  11/24/26`, i.e. 2026-11-24.

## What ran

1. `CRTSRCPF PRISMIA1/ZNWDDS`, one member per DDS, then `CRTPF` for ORDHDR, ORDDTL, CUSMST, PRDMST and CODTBL.
2. Members NWC620 and the five copybooks were added to the existing, empty `PRISMIA1/QCBLLESRC`.
   COPY without OF resolves to QCBLLESRC in *LIBL, so they could not go in a file of our own.
3. The synthetic data was loaded with SQL INSERT (`input-*.csv`).
4. `CRTBNDCBL PGM(PRISMIA1/NWC620) SRCFILE(PRISMIA1/QCBLLESRC) SRCMBR(NWC620)`: LNC0901, created.
   The listing has six severity-0 LNC0848 messages.
5. For each order: `OVRPRTF FILE(QSYSPRT) PAGESIZE(66 132) LPI(6) CPI(10) FORMTYPE(INVOICE)
   SPLFNAME(NWINVOICE) HOLD(*YES) SHARE(*NO) OVRSCOPE(*JOB)` (the NWC620C override, at job scope),
   then `CALL PGM(PRISMIA1/NWC620) PARM(X'nnnnnnnF')` (packed 7,0), then `DLTOVR`.
6. `CPYSPLF ... TOFILE(PRISMIA1/SPLOUT) CTLCHAR(*FCFC)`, then the lines were read back with SQL.
   SPLOUT was created with `RCDLEN(133)` and has CCSID 65535, so the bytes arrive unconverted.
   They were decoded as CCSID 37. Every character in the output is invariant between 37 and 273.
7. Everything was dropped (see Cleanup).

The full log is `run-log.txt` (the INSERT statements are left out). The programs are `pub400nwc620.go.txt`
(runner), `fixture.go.txt` (data), `goldencompare.go.txt` and `fixturecsv.go.txt`.

## Golden files

`order-NNNNNNN.fcfc.txt` holds one spooled file per order, exactly as CPYSPLF *FCFC wrote it. Each line
is 133 bytes: the FCFC control (`1` skip to a new page, ` ` one line, `0` two lines, `-` three lines)
followed by the 132-byte record. Trailing spaces are kept.

| Order | Case | Spool pages |
|-------|------|-------------|
| 1001 | 3 lines: discount 0.15 → `15%`, discount 9.99 (the PIC maximum of ODDISC 3P 2) → `999%`, negative extended amount, ship via found | 2 |
| 1002 | customer not on file, product not on file, ship via `9` not in CODTBL (a `REG 9` row exists), line number 0, order date 991231 (century 0) | 2 |
| 1003 | 46 lines: line 46 goes onto page 2. Order date 1000101 (century 1). Shipper description longer than HD3-SHPV X(30) | 3 |
| 1004 | 40 lines: the totals go onto page 2 | 3 |
| 1006 | not on ORDHDR: **no spooled file was created** (no golden file) | none |
| 1007 | header without lines: headings and totals only | 2 |
| 9999999 | edit-mask limits: order no. 9999999, quantity 99999, price 9999999.99, extended 999999999.99, line 999, discount −0.25 → `25%`, negative quantity and price print unsigned, order date 0 → `00/00/00`, address 60 → 40 truncation. WS-ITEMS overflows (ADD without SIZE ERROR): `76.54`. WS-DUE overflows: `99,999.98` | 2 |

Two things on the host are not NWC620 output:

- **PUB400 footer.** PUB400 adds the line `-=* https://pub400.com *=-` near the bottom of every page of
  every spooled file. It also appears on system compile listings.
- **Empty first page.** The invoices have one page more than their content because of IBM i
  behaviour, not PUB400. The first WRITE ... AFTER ADVANCING PAGE, at the open position (line 1),
  ejects an empty page 1. Control: two 20-line programs differ only in the first WRITE.
  `control-ZNWCP.fcfc.txt` (AFTER ADVANCING PAGE) has 2 pages: page 1 is empty and FIRST RECORD
  is on page 2. `control-ZNWCL.fcfc.txt` (AFTER ADVANCING 1 LINE) has 1 page.

## Comparison with the Go port

The frozen port at `nwc620-final/tree` ran from a scratch copy. Its application layer
(`application.NewService`) got an in-memory repository fed with the same records and a fixed clock
of 2026-11-24. The PrintLines are rendered as FCFC (NewPage `1`, Advance 1 ` `, Advance 2 `0`). Each
record gets a page and line number. The comparison checks the 133 bytes and the page and line
position of every record. PUB400's footer and the blank spacing lines are removed from the golden
side. A blank `1` line is kept, because the program caused it. Output: `compare-output.txt`.

Result: **every printed record of all six invoices is byte-identical and on the same page and line**
(168 records). There are 7 differences:

1. Orders 1001, 1002, 1003, 1004, 1007 and 9999999. ILE produces an empty page 1 before the first
   heading. The port's `printer.Render` drops the form feed before the very first line, so its
   spooled file has one page fewer.
2. Order 1006 (not on file). ILE leaves no spooled file at all. The port hands an empty spooled
   file to the printer, and its PORT-NOTE says "a missing order still closes an empty invoice
   spooled file". The host contradicts that note.

The arithmetic cases (discount truncation, the unsigned WS-DISC-PCT, ADD and COMPUTE overflow
truncation, the CYYMMDD conversion and the edit masks) all agree.

## Cleanup

Objects created: the files ORDHDR, ORDDTL, CUSMST, PRDMST, CODTBL, SPLOUT and ZNWDDS (with five
members), the programs NWC620, ZNWCP and ZNWCL, and the members NWC620, ORDHDRCP, ORDDTLCP,
CUSMSTCP, PRDMSTCP, CODTBLCP, ZNWCP and ZNWCL in QCBLLESRC. Spooled files in 446725/PRISMIA/QPRTJOB:
the compile and DDS listings and the NWINVOICE files. ZZALIAS was a temporary alias, dropped after
each member.

After cleanup, `OBJECT_STATISTICS('PRISMIA1','*ALL')` lists only the pre-existing, empty source
files QCBLLESRC, QCLSRC, QCMDSRC, QCPPSRC, QDDSSRC and QRPGLESRC. `SYSPARTITIONSTAT` shows no
member in PRISMIA1. The spooled files of PRISMIA are only PRZREF and PRZUSE. Those two predate this
run: they are DDS listings from the 2026-11-23 REFFLD check and were left untouched.

**R50, 2026-11-24.** PRZREF and PRZUSE (job 446725/PRISMIA/QPRTJOB, created 2026-11-23) were
deleted afterwards by the same i400/QCMDEXC path:

```
DLTSPLF FILE(PRZREF) JOB(446725/PRISMIA/QPRTJOB) SPLNBR(1)
DLTSPLF FILE(PRZUSE) JOB(446725/PRISMIA/QPRTJOB) SPLNBR(2)
```

Verification after the deletion:

- `QSYS2.OUTPUT_QUEUE_ENTRIES_BASIC WHERE USER_NAME = 'PRISMIA'`: no rows.
- `SELECT COUNT(*) FROM QSYS2.OUTPUT_QUEUE_ENTRIES WHERE USER_NAME = 'PRISMIA'`: 0.
- `OBJECT_STATISTICS('PRISMIA1','*ALL')`: QCBLLESRC, QCLSRC, QCMDSRC, QCPPSRC, QDDSSRC, QRPGLESRC (all *FILE PF).
- `SELECT COUNT(*) FROM QSYS2.SYSPARTITIONSTAT WHERE SYSTEM_TABLE_SCHEMA = 'PRISMIA1'`: 0 members.

Nothing here runs in CI.
