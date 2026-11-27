package plugin

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/npmregistry"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/source"
)

// Where the npm registry in use came from (RegistryInUse.From).
const (
	FromMagpie  = "magpie"  // the explicit upstream in Settings
	FromEnv     = "env"     // magpie's environment: BUN_CONFIG_REGISTRY, NPM_CONFIG_REGISTRY
	FromShell   = "shell"   // the login shell's, which magpie opened from the Dock doesn't have
	FromBunfig  = "bunfig"  // install.registry in a .bunfig.toml
	FromNPMRC   = "npmrc"   // registry= in an .npmrc
	FromChina   = "china"   // the 「国内镜像」 switch: npmmirror
	FromDefault = "default" // npm's own
)

// RegistryInUse is the default registry for plugins and where it came
// from. Var names the environment variable and Path the config file.
type RegistryInUse struct {
	URL  string `json:"url"`
	From string `json:"from"`
	Var  string `json:"var,omitempty"`
	Path string `json:"path,omitempty"`
}

// upstream is the explicit setting, or empty in automatic mode.
func (r RegistryInUse) upstream() string {
	if r.From == FromMagpie {
		return r.URL
	}
	return ""
}

// registryVars are the variables bun takes a registry from, in order.
var registryVars = []string{"BUN_CONFIG_REGISTRY", "NPM_CONFIG_REGISTRY", "npm_config_registry"}

// loginEnv is what the login shell said of a variable (a var for tests).
var loginEnv = proc.LoginEnvKnown

// Registry is the effective default for plugins: the explicit upstream,
// then the user's own Bun setup, then the China mirror switch or npm's own.
func Registry() RegistryInUse {
	if u := npmregistry.Configured(); u != "" {
		return RegistryInUse{URL: u, From: FromMagpie}
	}
	if r, ok := userRegistry(); ok {
		return r
	}
	if source.China() {
		return RegistryInUse{URL: source.NPMMirror, From: FromChina}
	}
	return RegistryInUse{URL: npmregistry.Default, From: FromDefault}
}

// userRegistry is the registry the user's own setup names, as bun finds it.
func userRegistry() (RegistryInUse, bool) {
	for _, k := range registryVars {
		if u, err := npmregistry.Clean(os.Getenv(k)); err == nil && u != "" {
			return RegistryInUse{URL: u, From: FromEnv, Var: k}, true
		}
	}
	for _, k := range registryVars {
		if u, err := npmregistry.Clean(loginEnv(k)); err == nil && u != "" {
			return RegistryInUse{URL: u, From: FromShell, Var: k}, true
		}
	}
	for _, f := range registryFiles() {
		var u string
		if f.bunfig {
			u = bunfigRegistry(f.path)
		} else {
			u = npmregistry.FileRegistry(f.path)
		}
		if u, err := npmregistry.Clean(u); err == nil && u != "" {
			from := FromNPMRC
			if f.bunfig {
				from = FromBunfig
			}
			return RegistryInUse{URL: u, From: from, Path: f.path}, true
		}
	}
	return RegistryInUse{}, false
}

type registryFile struct {
	path   string
	bunfig bool
}

// registryFiles are the files bun reads a registry from, the plugins
// folder's (where it installs) before the user's.
func registryFiles() []registryFile {
	out := []registryFile{{filepath.Join(Dir(), "bunfig.toml"), true}, {filepath.Join(Dir(), ".npmrc"), false}}
	if x := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		out = append(out, registryFile{filepath.Join(x, ".bunfig.toml"), true})
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, registryFile{filepath.Join(home, ".bunfig.toml"), true}, registryFile{filepath.Join(home, ".npmrc"), false})
	}
	return out
}

// SetRegistry saves the same Magpie-wide upstream as Settings does.
// "default" or an empty value restores automatic selection.
func SetRegistry(u string) (RegistryInUse, error) {
	if strings.EqualFold(strings.TrimSpace(u), "default") {
		u = ""
	}
	c, err := npmregistry.Clean(u)
	if err != nil {
		return Registry(), err
	}
	changed, err := settings.SetNPMRegistry(c)
	if err != nil {
		return Registry(), err
	}
	if changed {
		ReloadInfo()
	}
	return Registry(), nil
}

type registryContextKey struct{}

func withRegistry(ctx context.Context, r RegistryInUse) context.Context {
	return context.WithValue(ctx, registryContextKey{}, r)
}

func registryFor(ctx context.Context) RegistryInUse {
	if r, ok := ctx.Value(registryContextKey{}).(RegistryInUse); ok {
		return r
	}
	return Registry()
}

// registryEnv is Bun's child environment. In automatic mode Bun reads its
// own configuration, except for a registry found only in the login shell
// or chosen through the China mirror switch.
func registryEnv(ctx context.Context, env []string) []string {
	r := registryFor(ctx)
	switch r.From {
	case FromMagpie:
		return npmregistry.WithEnv(env, r.URL)
	case FromShell, FromChina:
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			if k = strings.ToUpper(k); (k == "BUN_CONFIG_REGISTRY" || k == "NPM_CONFIG_REGISTRY") && strings.TrimSpace(v) != "" {
				return env
			}
		}
		return append(env, "BUN_CONFIG_REGISTRY="+r.URL+"/", "NPM_CONFIG_REGISTRY="+r.URL+"/")
	}
	return env
}

// bunfigRegistry is install.registry in the bunfig at path: an address,
// or a table with its url; "" when it names none.
func bunfigRegistry(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c struct {
		Install struct {
			Registry any `toml:"registry"`
		} `toml:"install"`
	}
	if toml.Unmarshal(b, &c) != nil {
		return ""
	}
	switch r := c.Install.Registry.(type) {
	case string:
		return r
	case map[string]any:
		u, _ := r["url"].(string)
		return u
	}
	return ""
}

// npmBase returns the registry for a package and whether it uses the
// automatic public mirror policy rather than a user-chosen address.
func npmBase(r RegistryInUse, name string) (base, token string, official bool) {
	base, token, scoped := npmregistry.Resolve(r.URL, name)
	if !scoped && (r.From == FromDefault || r.From == FromChina) {
		return npmRegistry, "", true
	}
	return base, token, false
}

func fetchNPMFrom(ctx context.Context, r RegistryInUse, name, suffix string, limit int64) ([]byte, error) {
	base, token, official := npmBase(r, name)
	if official {
		return fetchJSON(ctx, base+suffix, limit)
	}
	return fetchRegistry(ctx, base, suffix, token, r.upstream() != "", limit)
}

func fetchRegistry(ctx context.Context, base, suffix, token string, strict bool, limit int64) ([]byte, error) {
	ctx = netproxy.Downloads(ctx)
	u := base + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	res, err := npmregistry.DoAt(http.DefaultClient, req, base, token, strict)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, &statusError{URL: u, Code: res.StatusCode, Status: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}
