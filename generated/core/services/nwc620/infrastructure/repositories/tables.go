// Package repositories reads the customer's existing Db2 for i files through
// milton_prism/pkg/db2access. The tables and columns are the schema facts of the
// analysis (DDS physical files); no DDL is ever issued.
package repositories

import (
	"errors"
	"fmt"

	"milton_prism/pkg/db2access"
)

// PORT-NOTE: [Record layouts come from the DDS because the copybooks are not provided]
// NWC620 declares its records with COPY ORDHDRCP, ORDDTLCP, CUSMSTCP, PRDMSTCP and
// CODTBLCP, which are not in the analysed source. The port assumes each COBOL item maps
// to the DDS field of the same meaning (OH-ORD-NO to OHORD#, OD-KEY to ODORD#, ODLIN#,
// CT-KEY to CTTYPE, CTCODE, ...); unverified against the copybooks.
//
// PORT-NOTE: [Input-only reads take no record locks, as OPEN INPUT does] Every file is
// opened INPUT, so the source never locked a record; the port issues plain SELECTs. The
// isolation those reads get comes from DB2_COMMIT_MODE, which the operator chooses (the
// source's commit mode was not measured).
//
// Tables are the physical files NWC620 opens for input. They carry no schema: under
// SQL naming the connection resolves them in its CURRENT SCHEMA, DB2_LIBRARY.
type Tables struct {
	OrdHdr db2access.Table
	OrdDtl db2access.Table
	CusMst db2access.Table
	PrdMst db2access.Table
	CodTbl db2access.Table
}

// NewTables declares the five files from the schema facts, types copied verbatim.
// Every DDS field without ALWNULL is NOT NULL WITH DEFAULT: blanks for character,
// zeros for numeric (no DFT keyword in the DDS).
func NewTables() (Tables, error) {
	var errs []error
	col := func(c db2access.Column, err error) db2access.Column {
		if err != nil {
			errs = append(errs, err)
		}
		return c
	}
	tbl := func(t db2access.Table, err error) db2access.Table {
		if err != nil {
			errs = append(errs, err)
		}
		return t
	}
	var ts Tables
	ts.CodTbl = tbl(db2access.NewTable("", "CODTBL",
		col(db2access.ColumnFromFacts("CTTYPE", "CHAR(3)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CTCODE", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CTDESC", "CHAR(60)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CTALT1", "CHAR(30)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CTALT2", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
	))
	ts.CodTbl = tbl(ts.CodTbl.WithUniqueKey([]string{"CTTYPE", "CTCODE"}))
	ts.CusMst = tbl(db2access.NewTable("", "CUSMST",
		col(db2access.ColumnFromFacts("CMCUST", "CHAR(5)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMNAME", "CHAR(40)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMCONT", "CHAR(30)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMTITL", "CHAR(30)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMADDR", "CHAR(60)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMCITY", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMREGN", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMPOST", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMCNTY", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMPHON", "CHAR(24)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMFAX", "CHAR(24)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMSTAT", "CHAR(1)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("CMCRDT", "NUMERIC(6, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("CMCHDT", "NUMERIC(6, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("CMCHUS", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
	))
	ts.CusMst = tbl(ts.CusMst.WithUniqueKey([]string{"CMCUST"}))
	ts.OrdDtl = tbl(db2access.NewTable("", "ORDDTL",
		col(db2access.ColumnFromFacts("ODORD#", "DECIMAL(7, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODLIN#", "DECIMAL(3, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODPRD#", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODQTY", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODPRIC", "DECIMAL(9, 2)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODDISC", "DECIMAL(3, 2)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("ODEXT", "DECIMAL(11, 2)", "NOT_NULL", true, "ZEROS")),
	))
	ts.OrdDtl = tbl(ts.OrdDtl.WithUniqueKey([]string{"ODORD#", "ODLIN#"}))
	ts.OrdHdr = tbl(db2access.NewTable("", "ORDHDR",
		col(db2access.ColumnFromFacts("OHORD#", "DECIMAL(7, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHCUST", "CHAR(5)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHEMP#", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHODAT", "DECIMAL(7, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHRDAT", "DECIMAL(7, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHSDAT", "DECIMAL(7, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHSHPV", "CHAR(1)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHFRT", "DECIMAL(7, 2)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHSNAM", "CHAR(40)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHSADR", "CHAR(60)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHSCTY", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHSREG", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHSPST", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHSCNT", "CHAR(15)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHTOTL", "DECIMAL(11, 2)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("OHSTAT", "CHAR(1)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHENTU", "CHAR(10)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("OHENTD", "NUMERIC(6, 0)", "NOT_NULL", true, "ZEROS")),
	))
	ts.OrdHdr = tbl(ts.OrdHdr.WithUniqueKey([]string{"OHORD#"}))
	ts.PrdMst = tbl(db2access.NewTable("", "PRDMST",
		col(db2access.ColumnFromFacts("PMPRD#", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMNAME", "CHAR(40)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("PMSUP#", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMCATG", "NUMERIC(3, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMQPU", "CHAR(20)", "NOT_NULL", true, "BLANKS")),
		col(db2access.ColumnFromFacts("PMPRIC", "DECIMAL(9, 2)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMSTCK", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMONOR", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMROLV", "DECIMAL(5, 0)", "NOT_NULL", true, "ZEROS")),
		col(db2access.ColumnFromFacts("PMDISC", "CHAR(1)", "NOT_NULL", true, "BLANKS")),
	))
	ts.PrdMst = tbl(ts.PrdMst.WithUniqueKey([]string{"PMPRD#"}))
	if err := errors.Join(errs...); err != nil {
		return Tables{}, fmt.Errorf("nwc620: table declarations: %w", err)
	}
	return ts, nil
}

// All lists the tables (for a test fake's catalogue).
func (t Tables) All() []db2access.Table {
	return []db2access.Table{t.OrdHdr, t.OrdDtl, t.CusMst, t.PrdMst, t.CodTbl}
}
