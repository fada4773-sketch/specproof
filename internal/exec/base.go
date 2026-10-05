package exec

import (
	"fmt"
	"net/url"
	"strings"
)

// Base returns the base URL for requests (FR-HTTP-06).
//
// With a configured BaseURL it replaces the spec's servers entirely; a
// warning is returned if the servers' base path is missing from it. With an
// in-process handler (serverURL set, baseURL empty) the servers' base path is
// appended, because the application is expected to serve its routes there.
func Base(baseURL, serverURL, specBasePath string) (base, warning string, err error) {
	if baseURL == "" {
		return strings.TrimSuffix(serverURL, "/") + specBasePath, "", nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("BaseURL %q is not an absolute URL (example: http://127.0.0.1:8080/api)", baseURL)
	}
	base = strings.TrimSuffix(baseURL, "/")
	if specBasePath != "" && !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), specBasePath) {
		warning = fmt.Sprintf("The spec declares base path %q in servers, but BaseURL %q does not contain it. Requests go to BaseURL + operation path.", specBasePath, baseURL)
	}
	return base, warning, nil
}
