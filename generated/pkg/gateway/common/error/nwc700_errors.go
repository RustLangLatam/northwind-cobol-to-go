package message_error

var nwc700ErrorMessages = map[string]string{
	// Domain errors (NW32xx)
	"NW3201": "The customer file holds more customers than the customer list can count (99999).",
	"NW3202": "The customer file is too large to read in one run.",
	"NW3203": "The database read limit is below what the customer list needs.",

	// Dependency errors (NW35xx)
	"NW3501": "The customer database is unavailable. Please try again later.",
	"NW3502": "The customer file is locked by another job. Please retry the run.",
	"NW3503": "The customer list could not be written to its output.",
}
