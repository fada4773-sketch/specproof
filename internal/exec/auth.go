package exec

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Placement is where a token goes in a request.
type Placement struct {
	In   string // "header", "query" or "cookie"
	Name string // header, parameter or cookie name
	// Bearer is set for "Authorization: Bearer <token>".
	Bearer bool
}

// Auth is the resolved authentication of an operation.
type Auth struct {
	// Placements is empty if the operation needs no token (FR-AUTH-04).
	Placements []Placement
	// Unsupported explains why no requirement can be fulfilled.
	Unsupported string
}

// Needed reports whether a token is sent.
func (a Auth) Needed() bool { return len(a.Placements) > 0 }

// ResolveAuth picks the first security requirement whose schemes are all
// supported (FR-AUTH-01, FR-AUTH-05). An empty requirement list or an empty
// requirement means no authentication.
func ResolveAuth(reqs openapi3.SecurityRequirements, schemes openapi3.SecuritySchemes) Auth {
	if len(reqs) == 0 {
		return Auth{}
	}
	var problems []string
	for _, req := range reqs {
		if len(req) == 0 {
			return Auth{}
		}
		names := make([]string, 0, len(req))
		for n := range req {
			names = append(names, n)
		}
		sort.Strings(names)
		var ps []Placement
		ok := true
		for _, n := range names {
			p, err := placement(n, schemes)
			if err != nil {
				problems = append(problems, err.Error())
				ok = false
				break
			}
			ps = append(ps, p)
		}
		if ok {
			return Auth{Placements: ps}
		}
	}
	return Auth{Unsupported: strings.Join(problems, "; ")}
}

func placement(name string, schemes openapi3.SecuritySchemes) (Placement, error) {
	ref := schemes[name]
	if ref == nil || ref.Value == nil {
		return Placement{}, fmt.Errorf("security scheme %q is not defined", name)
	}
	s := ref.Value
	switch s.Type {
	case "http":
		if strings.EqualFold(s.Scheme, "bearer") {
			return Placement{In: "header", Name: "Authorization", Bearer: true}, nil
		}
		return Placement{}, fmt.Errorf("security scheme %q (http/%s) is not supported, only http/bearer", name, s.Scheme)
	case "oauth2", "openIdConnect":
		return Placement{In: "header", Name: "Authorization", Bearer: true}, nil
	case "apiKey":
		switch s.In {
		case "header", "query", "cookie":
			return Placement{In: s.In, Name: s.Name}, nil
		}
		return Placement{}, fmt.Errorf("security scheme %q: apiKey in %q is not supported", name, s.In)
	default:
		return Placement{}, fmt.Errorf("security scheme %q of type %q is not supported", name, s.Type)
	}
}

// AllPlacements lists every placement defined by the spec's schemes, so that
// redaction knows all header, query and cookie names carrying tokens.
func AllPlacements(schemes openapi3.SecuritySchemes) []Placement {
	names := make([]string, 0, len(schemes))
	for n := range schemes {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Placement
	for _, n := range names {
		if p, err := placement(n, schemes); err == nil {
			out = append(out, p)
		}
	}
	return out
}
