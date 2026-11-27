# Evidence

This folder holds the proof for the code in [`../generated/`](../generated/): the signed archive, the signature checks, the golden output of the real COBOL programs, and the comparisons between the two. The root [`README.md`](../README.md) lists the results and the commands to check them.

**What was changed, and what was kept.** The files under `signature/` are unchanged. The PUB400 public address and the PUB400 user profile appear as produced, in the golden-run files. In `fidelity/`, labels that named internal builds were replaced: "this build" for the build label, "the earlier one-service build" for the earlier NWC620 build, "evidence/signature" for the original archive folder, and "the local environment" for the sandbox. The folder `fidelity-db2/` (the real Db2 for i run of 2026-11-27) was sanitised the same way: "this build" for the build label, "the earlier one-service build" for the earlier build, and `<workdir>`, `<repo>` and `<password-file>` for local paths. The golden NWC700 README gained a sentence on the equal-key sort, and one en dash in the golden NWC620 README was replaced by "none".

## How the folders relate

```
signature/   the signed archive and the record that signs it
   |          manifest.json lists the 169 files; the archive is their unpacked form
   v
fidelity/    the comparisons of the generated services against the golden output
   ^
   |
golden-nwc620/, golden-nwc700/   the real COBOL output, from PUB400, with its inputs
```

`check_manifest.py` (this folder) checks the unpacked tree against `signature/manifest.json`. The signed statement names both `deliverable.zip` and `manifest.json` as subjects. `verify_attestation.py` checks the archive's hash against its subject. The manifest's hash (`1c94ee22…`) was checked by hand with `sha256sum`.

## signature/

The signed verification record of the deliverable. Its id is `2448adb23c741e79`. The files named `attestation-*` are the files of this record. The record is not an attestation: the full-plan policy was not met, because only 2 of 13 plan services were generated. Its issuer is the first party that produced the code, not a third party.

| File | What it is |
|------|------------|
| `deliverable.zip` | The signed archive. Its SHA-256 is `7c51d0b491b5d085f93be7665c699da384a9b054ed8fa74946352c1c32b9c6a1`. It holds the 169 files of `../generated/` (verified by `diff -r`, see the root README). |
| `manifest.json` | The file list: each path, size, SHA-256 and origin. The signed record names its SHA-256 (`1c94ee22…`) as a second subject. |
| `attestation-2448adb23c741e79.json` | The verification record in plain JSON: the id, issue time, key id, subject hashes, each claim and its status. Its `verificationResult` is `FAILED`. |
| `attestation-2448adb23c741e79-envelope.raw` | The signed statement (DSSE envelope, `application/vnd.in-toto+json`). It has one Ed25519 signature over the statement. Its claim statuses and archive hash match the JSON record. |
| `attestation-2448adb23c741e79-bundle.jsonl` | The signed bundle: three statements, each with a valid signature. |
| `attestation_keys.json` | The public Ed25519 key that verifies the signatures. Its fingerprint is the SHA-256 of the raw key bytes; the verifier checks it. |
| `verify_attestation.py` | The verifier. It checks the key fingerprint, the signatures, and that the signed subject is the archive. It prints the claims. |

The verifier opens the archive under the name `deliverable.zip.raw`. In this folder the file is named `deliverable.zip`, so the root README copies the folder and renames the archive before the call.

## fidelity/

The comparison of the generated services against the real COBOL output. The method and the Db2 status are in [`fidelity/README.md`](fidelity/README.md).

| File | What it is |
|------|------------|
| `README.md` | The method, the rules for each comparison, the in-memory results, and the real Db2 for i results (2026-11-27). |
| `goldencompare620.go.txt` | Runner. It feeds the NWC620 test data to the service in memory and compares the spool output with the golden invoices. Usage: `goldencompare620 <golden dir>`. |
| `goldencompare700.go.txt` | Runner. The same for NWC700, against the three customer sets. Usage: `goldencompare700 <golden dir>`. |
| `nwc620-golden-compare-output.txt` | The stored output of the NWC620 runner: 168 of 168 identical. |
| `nwc700-golden-compare-output.txt` | The stored output of the NWC700 runner: 186 of 188 identical, with the two differing records printed. |
| `db2realrun620.go.txt` | Runner for NWC620 against Db2 for i on PUB400. It ran on 2026-11-27 (see [`fidelity-db2/`](fidelity-db2/)). It takes a password file and a DDS directory. |
| `db2realrun700.go.txt` | Runner for NWC700 against Db2 for i on PUB400. It ran on 2026-11-27 (see [`fidelity-db2/`](fidelity-db2/)). |

## fidelity-db2/

The real Db2 for i run of 2026-11-27: NWC620 and NWC700 against PUB400 through their own Db2 adapter. The method, the results and the cleanup are in [`fidelity-db2/README.md`](fidelity-db2/README.md). It holds the two run logs and the two comparison outputs. The runners are the two in `fidelity/`.

The `.go.txt` suffix keeps these runners out of the Go build. To run them, copy them into a copy of `generated/` (see the root README, step 6).

## golden-nwc620/

Real output of the ILE COBOL program NWC620, compiled and run on PUB400 on 2026-11-24 with synthetic data.

| File | What it is |
|------|------------|
| `README.md` | How the program was compiled and run, the cases each order covers, and the cleanup of the host. |
| `input-*.csv` | The synthetic data loaded into the five tables: customer, code, order detail, order header, product. |
| `order-NNNNNNN.fcfc.txt` | The spooled invoice for each printed order, as the host wrote it. Each line starts with the carriage control byte. Orders 1001, 1002, 1003, 1004, 1007 and 9999999. |
| `control-ZNWCP.fcfc.txt`, `control-ZNWCL.fcfc.txt` | Two control programs. They show that the empty first page comes from the way the host starts a page, not from the program's logic. |
| `compare-output.txt` | The output of the earlier comparison with a previous build. Superseded by `fidelity/`. |
| `goldencompare.go.txt`, `fixture.go.txt`, `fixturecsv.go.txt` | The earlier comparison runner and the test data it used. Superseded by `fidelity/goldencompare620.go.txt`. |
| `pub400nwc620.go.txt` | The program that loaded the data and ran the COBOL program on the host. |
| `run-log.txt` | The log of the host run, with the INSERT statements left out. |

Some of these files still name internal task labels (R46, R48, R49, R50) and a path in the original workspace (`docs/openspec/...`, `nwc620-final/tree`). They were not replaced and are kept as produced.

## golden-nwc700/

Real output of the OPM COBOL/400 program NWC700, compiled and run on PUB400 on 2026-11-25 with synthetic data.

| File | What it is |
|------|------------|
| `README.md` | How the program was compiled and run, the three data sets, and the cleanup of the host. |
| `input-cusmst-edge.csv`, `input-cusmst-empty.csv`, `input-cusmst-pages.csv` | The synthetic customer data: 11 rows (edge cases), none (empty file), and 148 rows (page breaks). |
| `nwc700-<set>.fcfc.ebcdic` | The spooled report for each set, exactly as the host wrote it (EBCDIC). |
| `nwc700-<set>.fcfc.txt` | The same report decoded as CCSID 37. Every character is the same in CCSID 37 and 273. |
| `decode-cp037.py.txt` | The script used to decode the EBCDIC files. |
| `pub400nwc700.go.txt` | The program that loaded each data set and ran the COBOL program on the host. |
| `run-log.txt` | The log of the host run. |

Some of these files still mention a relative path in the original workspace (for example `../pub400-nwc620-golden-2026-11-24/`). They are kept as produced.

## Other notes

- `generation-attempts.md` lists the generation attempts that the platform's gates refused, and the attempts that passed. Its facts come from the platform's generation-worker logs and run records, which are not public.
- The root README gives the command for each check. The runners and the verifier can be run with the commands given there.
- Files written by the upstream author (the COBOL source, the DDS, the copybooks) are not in this folder. They are at the commit named in the root README.
