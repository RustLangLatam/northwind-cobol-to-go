package domain

import "milton_prism/core/shared/domainerr"

// Error codes of the NWC700 service (prefix NW3, assigned by the registry). The code
// opens the public message so the gateway can find its friendly text.
const (
	ErrCodeTooManyCustomers = "NW3201"
	ErrCodeResultTooLarge   = "NW3202"
	ErrCodeReadLimit        = "NW3203"
	ErrCodeStoreUnavailable = "NW3501"
	ErrCodeStoreBusy        = "NW3502"
	ErrCodePrintFailed      = "NW3503"
)

// TooManyCustomers refuses a run whose count would overflow the PIC 9(5) counters.
func TooManyCustomers() error {
	return domainerr.New(domainerr.ResourceExhausted,
		ErrCodeTooManyCustomers+" Failure_Customer_List_Too_Large: CUSMST holds more than 99999 customers, the most the list counts (PIC 9(5)); the valid range is 0 to 99999")
}

// ResultTooLarge reports a customer read above the store's memory bound.
func ResultTooLarge(cause error) error {
	return domainerr.Wrap(domainerr.ResourceExhausted,
		ErrCodeResultTooLarge+" Failure_Customer_Read_Too_Large: the customer file is too large to read at once", cause)
}

// ReadLimitTooLow reports a DB2_MAX_LIMIT below the read the list needs.
func ReadLimitTooLow(cause error) error {
	return domainerr.Wrap(domainerr.FailedPrecondition,
		ErrCodeReadLimit+" Failure_Customer_Read_Limit: DB2_MAX_LIMIT is below the 100000 rows the customer list reads", cause)
}

// StoreUnavailable reports that CUSMST cannot be read now.
func StoreUnavailable(cause error) error {
	return domainerr.Wrap(domainerr.Unavailable, ErrCodeStoreUnavailable+" Failure_Customer_Store_Unavailable", cause)
}

// StoreBusy reports a lock timeout or deadlock on CUSMST: the run may be retried.
func StoreBusy(cause error) error {
	return domainerr.Wrap(domainerr.Aborted,
		ErrCodeStoreBusy+" Failure_Customer_Store_Busy: CUSMST is locked by another job; retry the run", cause)
}

// PrintFailed reports that the report could not be written to its output.
func PrintFailed(cause error) error {
	return domainerr.Wrap(domainerr.Unavailable, ErrCodePrintFailed+" Failure_Report_Output_Unavailable", cause)
}
