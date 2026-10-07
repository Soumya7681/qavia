package datagen

import "strings"

// Payment card numbers (BE-8.4, F-10.4).
//
// This file exists because the obvious implementation is dangerous. Every faker
// library ships a credit-card generator; they produce Luhn-valid numbers in real
// issuer ranges, which is to say numbers that may belong to somebody. Feeding one into
// a client's staging environment can authorise a real charge attempt, and putting one
// in a test fixture puts it in version control.
//
// So the platform generates none. It draws from a curated list of numbers the card
// networks and payment processors publish specifically for testing, and
// AllowedCardNumbers is the whole of what this module will ever emit. A test asserts
// that every generated PAN is in this set, which is what makes the rule enforceable
// rather than a comment somebody deletes in a hurry (BE-8.4.2).

// AllowedCardNumbers are the published test numbers this platform may emit.
//
// Sources: Stripe's documented test cards, and the network test numbers Visa,
// Mastercard, American Express, and Discover publish for the same purpose. Nothing is
// computed; the list is data, so adding to it is a review of a constant rather than a
// change to a generator.
var AllowedCardNumbers = []string{
	// Stripe test cards, by brand.
	"4242424242424242", // Visa
	"4000056655665556", // Visa debit
	"5555555555554444", // Mastercard
	"2223003122003222", // Mastercard (2-series)
	"5200828282828210", // Mastercard debit
	"378282246310005",  // American Express
	"371449635398431",  // American Express
	"6011111111111117", // Discover
	"6011000990139424", // Discover
	"3056930009020004", // Diners Club
	"3566002020360505", // JCB

	// Stripe's documented decline and error cards. A test suite that only ever sees a
	// successful payment is a suite that has never exercised the failure path, which is
	// where the money actually goes missing.
	"4000000000000002", // generic decline
	"4000000000009995", // insufficient funds
	"4000000000000069", // expired card
	"4000000000000127", // incorrect CVC
	"4000000000000119", // processing error
}

// declineCards are the subset that a processor is documented to refuse. Named so a
// generator can ask for a failure deliberately rather than by drawing until it gets
// one.
var declineCards = []string{
	"4000000000000002",
	"4000000000009995",
	"4000000000000069",
	"4000000000000127",
	"4000000000000119",
}

// AllowedCard reports whether a number is one this platform is permitted to emit.
//
// Separators are ignored, because a generated record may format the number for display
// and the rule is about the digits.
func AllowedCard(number string) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, number)

	for _, allowed := range AllowedCardNumbers {
		if digits == allowed {
			return true
		}
	}
	return false
}

// cardFieldMarkers are the fragments that mean "a payment card number".
//
// Matched as substrings of the separator-stripped name, not by exact equality, because
// a schema names this field a dozen ways — cardNumber, card_no, creditCardNo,
// paymentCardNumber, ccNum — and the one that slips through the list is the one that
// gets a freshly invented Luhn-valid number. The list is deliberately about the
// *number*: `cardHolderName` and `cardBrand` are not card numbers and match nothing
// here.
//
// Matched on the name because a schema rarely says more: `type: string,
// maxLength: 19` describes a card number and a warehouse SKU identically, and only one
// of those must never be invented.
var cardFieldMarkers = []string{
	"cardnumber", "cardnum", "cardno",
	"creditcard", "debitcard", "paymentcard",
	"ccnumber", "ccnum", "ccno",
}

// exactCardFieldNames are the short names that mean a card number on their own and
// would be reckless as substrings: `pan` appears inside `panel` and `company`.
var exactCardFieldNames = []string{"pan", "card"}

// isCardField reports whether a field name means a payment card number.
func isCardField(name string) bool {
	normalised := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))

	for _, exact := range exactCardFieldNames {
		if normalised == exact {
			return true
		}
	}
	for _, marker := range cardFieldMarkers {
		if strings.Contains(normalised, marker) {
			return true
		}
	}
	return false
}
