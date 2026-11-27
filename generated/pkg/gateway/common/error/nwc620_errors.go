package message_error

var nwc620ErrorMessages = map[string]string{
	// Validation errors (NW21xx)
	"NW2101": "The order number must be between 1 and 9999999.",
	"NW2102": "A value contains a character the database cannot store.",

	// Domain errors (NW22xx, NW23xx)
	"NW2201": "The order was not found.",
	"NW2301": "The order is locked by another job. Please try again.",

	// Internal errors (NW25xx)
	"NW2500": "An internal invoice error occurred. Please try again later.",
	"NW2501": "The order database is unavailable. Please try again later.",
	"NW2502": "The invoice could not be written to its output. Please try again later.",
}
