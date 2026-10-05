package value

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// persons are DTO name parts whose "Name" is a person's name.
var persons = []string{"user", "person", "customer", "employee", "owner", "manager", "contact", "member", "author", "account", "admin", "staff"}

// semantic returns a realistic value for a field name, e.g. a city for
// "City" or a person's name for User.Name. Names are matched by their
// lower-case form; false means the name carries no meaning to use.
func (g *generator) semantic(name string) (string, bool) {
	ws := Words(name)
	parent := strings.ToLower(g.parent)
	f := g.fake
	// has reports whether one of the words is part of the name, as whole
	// words: "ReportCode" contains "code", not "ort"
	has := func(candidates ...string) bool {
		for _, c := range candidates {
			if slices.Contains(ws, c) || strings.Join(ws, "") == c {
				return true
			}
		}
		return false
	}
	last := ""
	if len(ws) > 0 {
		last = ws[len(ws)-1]
	}
	switch {
	case len(ws) == 0:
		return "", false
	case has("firstname", "givenname", "forename", "vorname") || (has("first", "given") && last == "name"):
		return f.FirstName(), true
	case has("lastname", "surname", "familyname", "nachname") || (has("last", "family") && last == "name"):
		return f.LastName(), true
	case has("username", "login", "nickname") || (has("user") && last == "name"):
		return f.Username(), true
	case has("fullname", "displayname") || (has("full", "display", "contact", "person") && last == "name"):
		return f.Name(), true
	case has("company", "organisation", "organization", "firma") || (has("tenant") && last == "name"):
		return f.Company(), true
	case has("jobtitle") || (has("job") && last == "title"):
		return f.JobTitle(), true
	case has("street", "strasse", "address1", "addressline"):
		return f.Street(), true
	case has("city", "town", "stadt"):
		return f.City(), true
	case has("zip", "postal", "postcode", "plz"):
		return f.Zip(), true
	case has("country") && (last == "code" || has("iso")):
		return f.CountryAbr(), true
	case has("country") && last != "id":
		return f.Country(), true
	case has("province", "region") || (len(ws) == 1 && ws[0] == "state"):
		return f.State(), true
	case has("phone", "mobile", "telephone", "fax", "telefon"):
		return f.Phone(), true
	case has("currency"):
		return f.CurrencyShort(), true
	case has("language", "locale", "sprache"):
		return f.LanguageAbbreviation(), true
	case has("color", "colour", "farbe"):
		return f.Color(), true
	case last == "version":
		return f.AppVersion(), true
	case has("domain"):
		return f.DomainName(), true
	case has("description", "comment", "note", "remark", "message", "summary", "beschreibung"):
		return f.Sentence(), true
	case last == "title" || has("subject", "headline"):
		return title(f.Adjective() + " " + f.Noun()), true
	case last == "name":
		for _, p := range persons {
			if strings.Contains(parent, p) {
				return f.Name(), true
			}
		}
		if len(ws) > 1 {
			// CrewName in any DTO: "Quiet Crew"
			return title(f.Adjective() + " " + strings.Join(ws[:len(ws)-1], " ")), true
		}
		if g.parent != "" {
			// Name of a Garden: "Brave Garden"
			return title(f.Adjective()) + " " + dtoWords(g.parent), true
		}
		return title(f.Adjective() + " " + f.Noun()), true
	case has("product", "article"):
		return f.ProductName(), true
	case last == "code" || last == "key" || last == "number":
		return strings.ToUpper(f.LetterN(3)) + fmt.Sprint(100+g.rnd.IntN(900)), true
	}
	return "", false
}

// Words splits a field name into lower-case words: "MissionPlanId" →
// [mission plan id], "launch_pad-code" → [launch pad code].
func Words(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

func title(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}

// dtoSuffixes are dropped from DTO names in values: GardenRead → Garden.
var dtoSuffixes = []string{"read", "write", "upsert", "create", "update", "dto", "request", "response", "detail", "details", "model", "entity", "item"}

// dtoWords turns a DTO name into readable words without technical
// suffixes: "GreenGardenRead" → "Green Garden".
func dtoWords(s string) string {
	ws := Words(s)
	for len(ws) > 1 && slices.Contains(dtoSuffixes, ws[len(ws)-1]) {
		ws = ws[:len(ws)-1]
	}
	return title(strings.Join(ws, " "))
}
