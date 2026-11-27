# PUB400: NWC700 golden customer list from a real OPM COBOL/400 run (2026-11-25)

an earlier build generates Go services for NWC620 and NWC700. NWC620 has its golden in
`../pub400-nwc620-golden-2026-11-24/`. These files are the expected output of NWC700, built the
same way: the original program, compiled and run on a real IBM i, so the generated service can be
compared record by record.

## Host and source

- Host: PUB400.COM, IBM i 7.5, user PRISMIA. Only library PRISMIA1 was used. The connection used
  the i400 driver over the database host server (TLS 9471). CL commands ran through `QSYS2.QCMDEXC`.
  SSH and 5250 were not used.
- Source: https://github.com/ZodandSons/northwind-ibmi-cobol-legacy-application at commit
  `1b12bbb76c7176247abbe58dfd87da40549767c4`: `qcblsrc/nwc700.cbl` and the DDS `qddssrc/cusmst.pf`.
  The sources were uploaded unchanged. NWC700 has no copybooks: its CUSMST layout is typed inline.
- **NWC700 is not ILE COBOL.** It lives in `qcblsrc/` (not `qcbllesrc/`), and the repository's
  README, TRAINER-GUIDE and `setup/nwcbuild.clle` all say it was never moved off OPM COBOL/400. It
  was compiled with `CRTCBLPGM`, as `nwcbuild.clle` does. The object attribute is `CBL`, not `CBLLE`.
- Run: 2026-11-25, 16:10 to 16:11 host time. The job date the program used (ACCEPT FROM DATE) is
  printed in every heading as `11/25/26`.

## What NWC700 does

- No parameters. NWC700C only submits `CALL PGM(NWLCBL/NWC700)` to batch, with no `OVRPRTF`.
- Reads every CUSMST record in arrival sequence: no selection at all (the source says status D
  customers are not left out). Opens CUSMST INPUT only.
- SORTs on country, city, company name (ascending, EBCDIC collating sequence) and prints to
  `QSYSPRT`: the heading HD1/HD2 on a new page, a `COUNTRY:` line at each country break, one
  detail line per customer, `CUSTOMERS IN COUNTRY` per country and `TOTAL CUSTOMERS` at the end.
- Headings: a new page when the line count is > 50 at a country break, or > 55 before a detail
  line or before the grand total. The line count starts at 99, so the first record (or the grand
  total of an empty file) always gets a heading.

## What ran

1. `CRTSRCPF PRISMIA1/ZNWDDS` (member CUSMST, type PF) and `CRTSRCPF PRISMIA1/ZNWCBL` (member NWC700,
   type CBL), both `RCDLEN(92)`. Then `CRTPF PRISMIA1/CUSMST`: row length 291, the same as the
   inline CUS-REC.
2. `CRTCBLPGM PGM(PRISMIA1/NWC700) SRCFILE(PRISMIA1/ZNWCBL) SRCMBR(NWC700)`: LBL0901, created. The
   listing has two severity-00 LBL0650 messages.
3. For each data set: `DELETE FROM CUSMST`, one SQL INSERT per row in CSV order (so arrival
   sequence is the CSV order), then `CALL PGM(PRISMIA1/NWC700)`. QSYSPRT was not overridden, as in
   NWC700C: the spooled file is `QSYSPRT`, form type `*STD`, 66 x 132, 6 LPI, 10 CPI, user data NWC700.
4. `CPYSPLF ... TOFILE(PRISMIA1/SPLOUT) CTLCHAR(*FCFC)`, then the lines were read back with SQL.
   SPLOUT was created with `RCDLEN(133)` and has CCSID 65535, so the bytes arrive unconverted.
5. Everything was dropped (see Cleanup).

The full log is `run-log.txt`. The runner is `pub400nwc700.go.txt` (it lived in `cmd/pub400nwc700`
only for the run; the data sets are in it), the decoder is `decode-cp037.py.txt`.

## Golden files

`nwc700-<set>.fcfc.ebcdic` is the spooled file exactly as CPYSPLF *FCFC wrote it (EBCDIC).
`nwc700-<set>.fcfc.txt` is the same, decoded as CCSID 37; every character in it is invariant
between 37 and 273. Each line is 133 bytes: the FCFC control (`1` skip to a new page, ` ` one line,
`0` two lines, `-` three lines) followed by the 132-byte record. Trailing spaces are kept.

| Set | Input | Case | Printed records | Spool pages |
|-----|-------|------|-----------------|-------------|
| edge | `input-cusmst-edge.csv`, 11 rows | see below | 22 | 2 |
| empty | `input-cusmst-empty.csv`, 0 rows | empty file: heading and `TOTAL CUSTOMERS 0` only, no `CUSTOMERS IN COUNTRY` | 3 | 2 |
| pages | `input-cusmst-pages.csv`, 148 rows | every heading branch (below) | 163 | 5 |

Printed records exclude PUB400's footer and the blank spacing lines. 188 records in all.

**edge** (C0001 and C0002 are the NWC620 golden's CUSMST rows):

- Blank country (C0003, C0004) sorts first and still breaks from HIGH-VALUES: `COUNTRY:` with blanks.
- C0004 has every printed field blank; blank name sorts before `BLANK COUNTRY CO`.
- EBCDIC order: country `testland` (lower case) sorts before `FIFTEENCHARCNTY` and `TESTLAND`, and is
  its own country (the break is case-sensitive). In TESTLAND/TESTVILLE, `lower case name co` sorts
  before the upper-case names. City `1ST CITY` sorts after `TESTVILLE` (digits after letters).
  An ASCII sort would order all three the other way.
- C0005 has status D and is printed.
- C0009 and C0008 have equal keys (TESTLAND, TESTVILLE, `TIE CO`); they print in arrival order
  (C0009 first), so the sort kept input order for equal keys in this run. The upstream SORT has no `WITH DUPLICATES IN ORDER` clause, so COBOL does not define the relative order of records with equal keys. The host printed them in arrival order, the Go service by customer number; the source program guarantees neither. The statement is at [qcblsrc/nwc700.cbl, lines 115-116](https://github.com/ZodandSons/northwind-ibmi-cobol-legacy-application/blob/1b12bbb76c7176247abbe58dfd87da40549767c4/qcblsrc/nwc700.cbl#L115-L116).
- C0010 fills name 40, city 15, contact 30 and phone 24: the full 119-byte DL1, the rest of the
  line is blank. C0002 fills name 40, city 15 and country 15 (and a 60-byte address NWC700 does not print).

**pages**: AAPAGELAND 48, BBPAGELAND 52, CCPAGELAND 48 rows.

- AA ends at line count 53: TL1 is written, then the break (> 50) puts `COUNTRY: BBPAGELAND` on page 2.
- BB overflows on its 52nd detail (> 55): page 3 starts with the heading and `B0052`, with **no
  repeated `COUNTRY:` line**.
- CC ends at line count 56: TL1 stays on page 3 and `TOTAL CUSTOMERS 148` goes alone on page 4.

Two things on the host are not NWC700 output (same as the NWC620 golden):

- **PUB400 footer.** PUB400 adds `-=* https://pub400.com *=-` near the bottom of every page.
- **Empty first page.** Each spooled file has one page more than its content: the first WRITE
  ... AFTER ADVANCING PAGE at the open position ejects an empty page 1. NWC620's control programs
  showed this is IBM i behaviour; here it also holds for OPM COBOL.

## Cleanup

Objects created: the files CUSMST, SPLOUT, ZNWDDS (member CUSMST) and ZNWCBL (member NWC700), and
the program NWC700. Nothing was added to the pre-existing QCBLLESRC. Spooled files in
446725/PRISMIA/QPRTJOB: CUSMST (DDS listing, 35), NWC700 (compile listing, 36) and the three
QSYSPRT reports (37, 38, 39). ZZALIAS was a temporary alias, dropped after the member load.

Before the run PRISMIA had 0 spooled files and PRISMIA1 held only QCBLLESRC, QCLSRC, QCMDSRC,
QCPPSRC, QDDSSRC and QRPGLESRC, with no members. After cleanup (end of `run-log.txt`):

- `OBJECT_STATISTICS('PRISMIA1','*ALL')`: QCBLLESRC, QCLSRC, QCMDSRC, QCPPSRC, QDDSSRC, QRPGLESRC (all *FILE PF).
- `SELECT COUNT(*) FROM QSYS2.SYSPARTITIONSTAT WHERE SYSTEM_TABLE_SCHEMA = 'PRISMIA1'`: 0 members.
- `QSYS2.OUTPUT_QUEUE_ENTRIES_BASIC WHERE USER_NAME = 'PRISMIA'`: no rows;
  `SELECT COUNT(*) FROM QSYS2.OUTPUT_QUEUE_ENTRIES WHERE USER_NAME = 'PRISMIA'`: 0.

Nothing here runs in CI.
