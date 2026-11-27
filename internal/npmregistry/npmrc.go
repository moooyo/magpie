package npmregistry

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

type npmrc struct {
	registry string
	scopes   map[string]string
	tokens   map[string]string
}

// FileRegistry reads the default registry named by one .npmrc.
func FileRegistry(path string) string { return readNPMRC(path).registry }

// npmrcVar is a ${VAR} in an .npmrc's value, which npm expands.
var npmrcVar = lazyre.New(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

func readNPMRC(path string) npmrc {
	rc := npmrc{scopes: map[string]string{}, tokens: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return rc
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), npmrcVar.ReplaceAllStringFunc(strings.Trim(strings.TrimSpace(v), `"'`), func(m string) string { return os.Getenv(m[2 : len(m)-1]) })
		switch {
		case k == "registry":
			rc.registry = v
		case strings.HasPrefix(k, "@") && strings.HasSuffix(k, ":registry"):
			rc.scopes[strings.TrimSuffix(k, ":registry")] = v
		case strings.HasPrefix(k, "//") && strings.HasSuffix(k, ":_authToken"):
			rc.tokens[strings.TrimSuffix(k, ":_authToken")] = v
		}
	}
	return rc
}

// Resolve returns the upstream for name and the .npmrc token scoped to it.
// A named scope intentionally overrides the chosen default, as it does in
// a package manager. The plugins folder's .npmrc is read before the home's.
func Resolve(base, name string) (registry, token string, scoped bool) {
	rcs := []npmrc{readNPMRC(filepath.Join(settings.Dir(), "plugins", ".npmrc"))}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		rcs = append(rcs, readNPMRC(filepath.Join(home, ".npmrc")))
	}
	return resolve(rcs, base, name)
}

func userNPMRCPath() string {
	for _, key := range []string{"npm_config_userconfig", "NPM_CONFIG_USERCONFIG"} {
		if p := os.Getenv(key); filepath.IsAbs(p) {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".npmrc")
	}
	return ""
}

func userNPMRC() npmrc { return readNPMRC(userNPMRCPath()) }

// UserScope is a package's explicit scope registry from the user's config,
// or empty when the package uses the default.
func UserScope(name string) string {
	scope, _, ok := strings.Cut(name, "/")
	if !ok || !strings.HasPrefix(scope, "@") {
		return ""
	}
	base, _ := Clean(userNPMRC().scopes[scope])
	return base
}

// UserConfigUses reports an npmrc variable assigned by a specific server.
// A background install without the server's environment must not stand in
// for the command that reads that environment.
func UserConfigUses(env map[string]string) bool {
	if len(env) == 0 {
		return false
	}
	p := userNPMRCPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	for k := range env {
		if strings.Contains(string(b), "${"+k+"}") || strings.Contains(string(b), "${"+k+"?}") {
			return true
		}
	}
	return false
}

func resolve(rcs []npmrc, base, name string) (registry, token string, scoped bool) {
	if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		for _, rc := range rcs {
			if u, err := Clean(rc.scopes[scope]); err == nil && u != "" {
				base, scoped = u, true
				break
			}
		}
	}
	return base, tokenFor(rcs, base), scoped
}

// tokenFor uses the longest matching .npmrc nerf dart (//host/path/).
func tokenFor(rcs []npmrc, base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	dart := "//" + u.Host + strings.TrimRight(u.EscapedPath(), "/") + "/"
	best, tok := "", ""
	for _, rc := range rcs {
		for k, v := range rc.tokens {
			k = strings.TrimRight(k, "/") + "/"
			if strings.HasPrefix(dart, k) && len(k) > len(best) && v != "" {
				best, tok = k, v
			}
		}
	}
	return tok
}
