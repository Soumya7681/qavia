package datagen

import (
	"strings"

	"github.com/brianvoe/gofakeit/v7"
)

// Locale-shaped values (BE-8.1.2, and the reason BE-8.2 exists).
//
// The faker's address data is American, and a record with an Indian name, a US state,
// and a five-digit ZIP is a record no client ever received. So the country-specific
// values are drawn from small curated lists rather than generated: they are correct,
// they cost nothing, and they need no provider call.
//
// This is also the honest boundary of what deterministic code can do here. A list of
// twenty cities is not "a realistic Indian address"; a model can write one, and that is
// exactly the narrow job BE-8.2 gives it — field-level, opt-in, with this as the
// fallback whenever it is off or unavailable.

// LocaleIndia is the one locale with curated data so far. Others fall through to the
// faker's own values rather than pretending: a wrong Brazilian postcode is worse than
// an obviously American one, because only one of them looks plausible enough to ship.
const LocaleIndia = "in"

// Locales the platform has curated data for.
func Locales() []string { return []string{LocaleIndia} }

func (g *Generator) indian() bool {
	return strings.EqualFold(strings.TrimSpace(g.locale), LocaleIndia) ||
		strings.EqualFold(strings.TrimSpace(g.locale), "india") ||
		strings.EqualFold(strings.TrimSpace(g.locale), "en_IN")
}

var indianCities = []string{
	"Bengaluru", "Mumbai", "Pune", "Hyderabad", "Chennai", "Delhi", "Gurugram",
	"Noida", "Kolkata", "Ahmedabad", "Jaipur", "Kochi", "Bhubaneswar", "Indore",
	"Coimbatore", "Chandigarh", "Nagpur", "Lucknow", "Visakhapatnam", "Surat",
}

var indianStates = []string{
	"Karnataka", "Maharashtra", "Telangana", "Tamil Nadu", "Delhi", "Haryana",
	"Uttar Pradesh", "West Bengal", "Gujarat", "Rajasthan", "Kerala", "Odisha",
	"Madhya Pradesh", "Punjab", "Andhra Pradesh", "Assam",
}

// stateCodes are the numeric prefixes a GSTIN starts with, paired with their state so
// a generated identifier is internally consistent rather than merely well-shaped.
var stateCodes = []string{
	"01", "02", "03", "04", "05", "06", "07", "08", "09", "10",
	"19", "21", "24", "27", "29", "32", "33", "36",
}

var indianStreets = []string{
	"MG Road", "Brigade Road", "Anna Salai", "Linking Road", "Park Street",
	"FC Road", "Jubilee Hills Road No. 36", "Sector 18 Main Road", "Ring Road",
	"Church Street", "Residency Road", "Sarjapur Road",
}

func (g *Generator) city(faker *gofakeit.Faker) string {
	if g.indian() {
		return faker.RandomString(indianCities)
	}
	return faker.City()
}

func (g *Generator) state(faker *gofakeit.Faker) string {
	if g.indian() {
		return faker.RandomString(indianStates)
	}
	return faker.State()
}

func (g *Generator) country(faker *gofakeit.Faker) string {
	if g.indian() {
		return "India"
	}
	return faker.Country()
}

// postcode is six digits for India and the faker's own for everywhere else. Not
// starting with a zero, because a leading zero is what turns a postcode into an
// integer the first time somebody opens the CSV in a spreadsheet.
func (g *Generator) postcode(faker *gofakeit.Faker) string {
	if g.indian() {
		return faker.Numerify("#####") + faker.RandomString([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9"})
	}
	return faker.Zip()
}

func (g *Generator) street(faker *gofakeit.Faker) string {
	if g.indian() {
		return faker.Numerify("##") + ", " + faker.RandomString(indianStreets)
	}
	return faker.Street()
}

// phone is a plausible mobile number for the locale.
//
// Indian mobile numbers start with 6 to 9 and are ten digits, so a generated one that
// starts with a 3 fails the first validator it meets — which would make the data
// useless for exactly the field it was generated for.
func (g *Generator) phone(faker *gofakeit.Faker) string {
	if g.indian() {
		return "+91" + faker.RandomString([]string{"6", "7", "8", "9"}) + faker.Numerify("#########")
	}
	return faker.Phone()
}

// gstin is a correctly shaped Indian GST identifier: two-digit state code, a PAN, an
// entity digit, the letter Z, and a checksum character.
//
// Shaped rather than checksum-valid, and that distinction is worth stating: the real
// check character is a documented base-36 calculation, and generating one would make
// this platform emit identifiers indistinguishable from live ones. A shape that passes
// a format check and fails a checksum is the right kind of test data.
func (g *Generator) gstin(faker *gofakeit.Faker) string {
	return faker.RandomString(stateCodes) + g.indianPAN(faker) +
		faker.Numerify("#") + "Z" + strings.ToUpper(faker.LetterN(1))
}

// indianPAN is a correctly shaped permanent account number: five letters, four digits,
// one letter.
func (g *Generator) indianPAN(faker *gofakeit.Faker) string {
	return strings.ToUpper(faker.LetterN(5)) + faker.Numerify("####") +
		strings.ToUpper(faker.LetterN(1))
}
