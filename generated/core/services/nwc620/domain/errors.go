package domain

import (
	"fmt"

	"milton_prism/core/shared/domainerr"
)

// Error codes (prefix NW2 assigned by the orchestrator registry).
const (
	ErrCodeOrderNoOutOfRange = "NW2101"
	ErrCodeOrderNotFound     = "NW2201"
	ErrCodeTextNotStorable   = "NW2102"
	ErrCodeStoreBusy         = "NW2301"
	ErrCodeStoreUnavailable  = "NW2501"
	ErrCodeOutputUnavailable = "NW2502"
	ErrCodeInternal          = "NW2500"
)

// ErrOrderNoOutOfRange: LK-ORD-NO is outside the range of an existing order number.
func ErrOrderNoOutOfRange() error {
	return domainerr.New(domainerr.InvalidArgument,
		fmt.Sprintf("%s Failure_Order_Number_Out_Of_Range: lk_ord_no must be %d to %d (PIC S9(7), a positive order key)",
			ErrCodeOrderNoOutOfRange, MinOrderNo, MaxOrderNo))
}

// ErrOrderNotFound: no ORDHDR record has the order number.
func ErrOrderNotFound() error {
	return domainerr.New(domainerr.NotFound, ErrCodeOrderNotFound+" Failure_Order_Not_Found")
}

// ErrTextNotStorable: a key text holds a character the column's CCSID cannot represent.
func ErrTextNotStorable(cause error) error {
	return domainerr.Wrap(domainerr.InvalidArgument, ErrCodeTextNotStorable+" Failure_Text_Not_Representable", cause)
}

// ErrStoreBusy: a lock timeout or deadlock on the database; the caller may retry.
func ErrStoreBusy(cause error) error {
	return domainerr.Wrap(domainerr.Aborted, ErrCodeStoreBusy+" Failure_Record_Locked", cause)
}

// ErrStoreUnavailable: the database cannot be reached.
func ErrStoreUnavailable(cause error) error {
	return domainerr.Wrap(domainerr.Unavailable, ErrCodeStoreUnavailable+" Failure_Store_Unavailable", cause)
}

// ErrOutputUnavailable: the invoice could not be written to its output target.
func ErrOutputUnavailable(cause error) error {
	return domainerr.Wrap(domainerr.Unavailable, ErrCodeOutputUnavailable+" Failure_Output_Unavailable", cause)
}

// ErrInternal: a fault of the service itself (an invalid decimal, a bad picture).
func ErrInternal(cause error) error {
	return fmt.Errorf("%s Failure_Internal: %w", ErrCodeInternal, cause)
}
