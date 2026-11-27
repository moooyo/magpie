// Package npmregistry applies Magpie's explicit npm upstream to registry
// requests and to the package managers Magpie starts. An unset upstream
// leaves the caller's usual defaults in place.
package npmregistry

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/source"
)

// Default is npm's public registry.
const Default = "https://registry.npmjs.org"

// Configured is the explicit upstream, not a registry discovered from a
// shell or the China mirror switch. Its value can also key a caller's cache.
func Configured() string {
	u, _ := Clean(settings.Shared().NPMRegistry)
	return u
}

// Clean accepts a registry URL without credentials, query or fragment. An
// empty string means automatic selection; a subpath can name a private repo.
func Clean(u string) (string, error) {
	u = strings.TrimSpace(u)
	if u == "" {
		return "", nil
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Hostname() == "" || p.User != nil || p.ForceQuery || p.RawQuery != "" || strings.Contains(u, "#") {
		return "", errors.New("Enter an HTTP or HTTPS npm registry URL without credentials, a query, or a fragment.")
	}
	return strings.TrimRight(p.String(), "/"), nil
}

// Do sends a registry request to the snapshot of the selected upstream.
// Without an explicit upstream it uses the normal public mirror policy.
// With one, a named package can have its own scope in the user's .npmrc;
// a search has none. Plugin requests use their project .npmrc as well.
func Do(c *http.Client, req *http.Request, registry, name string) (*http.Response, error) {
	if registry == "" {
		return source.Do(c, req)
	}
	base, token, _ := resolve([]npmrc{userNPMRC()}, registry, name)
	suffix := req.URL.RequestURI()
	if strings.HasPrefix(name, "@") && strings.HasPrefix(suffix, "/"+name+"/") {
		// A scoped package is one segment in npm's registry protocol.
		suffix = "/" + strings.ReplaceAll(name, "/", "%2f") + strings.TrimPrefix(suffix, "/"+name)
	}
	u, err := url.Parse(base + suffix)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.URL, r.Host = u, ""
	return DoAt(c, r, base, token, true)
}

// DoAt sends the request to the selected registry, not to its mirrors. Only
// the matching npmrc token goes with it, and not to another origin or outside
// the registry's path. A custom upstream cannot redirect an automatic lookup
// to npm's public service; a scope explicitly set to npm's host can use it.
func DoAt(c *http.Client, req *http.Request, base, token string, strict bool) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	reg, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	client := *c
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if strict && !public(reg) && public(r.URL) {
			return errors.New("the configured npm registry redirected to npm's public service")
		}
		root := strings.TrimRight(reg.Path, "/") + "/"
		if !strings.EqualFold(r.URL.Host, reg.Host) || r.URL.Scheme != reg.Scheme || strict && !strings.HasPrefix(path.Clean(r.URL.Path)+"/", root) {
			r.Header.Del("Authorization")
		}
		if c.CheckRedirect != nil {
			return c.CheckRedirect(r, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return source.DoOfficial(&client, req)
}

func public(u *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return host == "npmjs.org" || host == "npmjs.com" || strings.HasSuffix(host, ".npmjs.org") || strings.HasSuffix(host, ".npmjs.com")
}
