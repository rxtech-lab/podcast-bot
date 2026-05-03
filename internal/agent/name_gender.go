package agent

import "strings"

// nameGender returns the conventional gender ("Male" / "Female") associated
// with a given first name, or "" if the name is unknown / ambiguous. The
// match is case-insensitive and only looks at the first whitespace-separated
// token, so "Bob" / "bob" / "Bob Smith" all hit the "bob" entry.
//
// Used by the voice picker to bias Azure voice selection toward voices that
// match the speaker's name (so "Bob" gets a male voice).
func nameGender(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if i := strings.IndexAny(n, " \t"); i >= 0 {
		n = n[:i]
	}
	return commonNameGender[n]
}

// commonNameGender covers the most frequent English given names plus a small
// set of names used in the project's example topics. Add to it as needed —
// unknown names just get no gender preference.
var commonNameGender = map[string]string{
	// Male
	"aaron": "Male", "adam": "Male", "alex": "Male", "alexander": "Male",
	"andrew": "Male", "anthony": "Male", "arthur": "Male", "austin": "Male",
	"benjamin": "Male", "bill": "Male", "bob": "Male", "brandon": "Male",
	"brian": "Male", "bruce": "Male", "carl": "Male", "charles": "Male",
	"chris": "Male", "christopher": "Male", "daniel": "Male", "dave": "Male",
	"david": "Male", "dennis": "Male", "donald": "Male", "douglas": "Male",
	"edward": "Male", "eric": "Male", "ethan": "Male", "frank": "Male",
	"fred": "Male", "gary": "Male", "george": "Male", "gerald": "Male",
	"greg": "Male", "gregory": "Male", "harold": "Male", "harry": "Male",
	"henry": "Male", "jack": "Male", "jacob": "Male", "james": "Male",
	"jason": "Male", "jeff": "Male", "jeffrey": "Male", "jeremy": "Male",
	"jerry": "Male", "jesse": "Male", "joe": "Male", "john": "Male",
	"jonathan": "Male", "joseph": "Male", "joshua": "Male", "justin": "Male",
	"keith": "Male", "kenneth": "Male", "kevin": "Male", "larry": "Male",
	"lawrence": "Male", "louis": "Male", "mark": "Male", "martin": "Male",
	"matthew": "Male", "michael": "Male", "mike": "Male", "nathan": "Male",
	"nicholas": "Male", "noah": "Male", "patrick": "Male", "paul": "Male",
	"peter": "Male", "philip": "Male", "raymond": "Male", "richard": "Male",
	"rick": "Male", "robert": "Male", "roger": "Male", "ronald": "Male",
	"ryan": "Male", "samuel": "Male", "scott": "Male", "sean": "Male",
	"stephen": "Male", "steve": "Male", "steven": "Male", "ted": "Male",
	"thomas": "Male", "tim": "Male", "timothy": "Male", "todd": "Male",
	"tom": "Male", "tony": "Male", "victor": "Male", "walter": "Male",
	"wayne": "Male", "william": "Male", "zachary": "Male",

	// Female
	"abigail": "Female", "alice": "Female", "amanda": "Female", "amelia": "Female",
	"amy": "Female", "andrea": "Female", "angela": "Female", "anna": "Female",
	"ashley": "Female", "ava": "Female", "barbara": "Female", "betty": "Female",
	"brenda": "Female", "carol": "Female", "carolyn": "Female", "catherine": "Female",
	"charlotte": "Female", "cheryl": "Female", "chloe": "Female", "christina": "Female",
	"christine": "Female", "claire": "Female", "cynthia": "Female", "deborah": "Female",
	"debra": "Female", "denise": "Female", "diana": "Female", "diane": "Female",
	"donna": "Female", "doris": "Female", "dorothy": "Female", "elizabeth": "Female",
	"ella": "Female", "ellen": "Female", "emily": "Female", "emma": "Female",
	"evelyn": "Female", "frances": "Female", "gloria": "Female", "grace": "Female",
	"hannah": "Female", "heather": "Female", "helen": "Female", "isabella": "Female",
	"jacqueline": "Female", "jane": "Female", "janet": "Female", "janice": "Female",
	"jean": "Female", "jennifer": "Female", "jessica": "Female", "joan": "Female",
	"joyce": "Female", "judith": "Female", "judy": "Female", "julia": "Female",
	"julie": "Female", "karen": "Female", "katherine": "Female", "kathleen": "Female",
	"kathy": "Female", "kelly": "Female", "kimberly": "Female", "laura": "Female",
	"lily": "Female", "linda": "Female", "lisa": "Female", "lori": "Female",
	"madison": "Female", "margaret": "Female", "maria": "Female", "marie": "Female",
	"marilyn": "Female", "martha": "Female", "mary": "Female", "megan": "Female",
	"melissa": "Female", "mia": "Female", "michelle": "Female", "nancy": "Female",
	"natalie": "Female", "nicole": "Female", "olivia": "Female", "pamela": "Female",
	"patricia": "Female", "paula": "Female", "rachel": "Female", "rebecca": "Female",
	"ruth": "Female", "samantha": "Female", "sandra": "Female", "sara": "Female",
	"sarah": "Female", "sharon": "Female", "shirley": "Female", "sophia": "Female",
	"stephanie": "Female", "susan": "Female", "teresa": "Female", "theresa": "Female",
	"tina": "Female", "victoria": "Female", "virginia": "Female",
}
