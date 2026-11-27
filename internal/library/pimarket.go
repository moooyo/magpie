package library

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/npmregistry"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
)

// Pi's package market (nineruiing on X). Pi's gallery, pi.dev/packages, is
// npm's packages with the keyword "pi-package", drawn on its server with no
// API of its own; magpie reads the list from npm's search and keeps it
// for six hours. Automatic mode can use its disk cache offline; a
// configured upstream never uses that cache or tries another registry.
//
// A package is installed and removed by Pi's own `pi install npm:<name>`
// and `pi remove`, run with PI_CODING_AGENT_DIR set to that Pi's agent
// folder (@earendil-works/pi-coding-agent 1.5.1, package-manager-cli.js and
// core/package-manager.js: installAndPersist installs it under
// <agent dir>/npm and adds its source to settings.json; removeAndPersist
// uninstalls it and takes out every entry of the same identity). A Pi
// whose command magpie can't run here, one in WSL or one without its CLI
// found, is given the entry alone, which Pi installs when it next starts
// (resolve → installMissing), as a copy from another Pi is.

// PiMarketPackage is one package the market offers.
type PiMarketPackage struct {
	// Source is what Pi installs it by: npm:<name>
	Source      string `json:"source"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	// Downloads is npm's count for the last month
	Downloads int    `json:"downloads,omitempty"`
	Icon      string `json:"icon,omitempty"`
	NPM       string `json:"npm"`
	Repo      string `json:"repo,omitempty"`
	Homepage  string `json:"homepage,omitempty"`
	Updated   string `json:"updated,omitempty"`
	// Kind is npm or git, and ID the package however its source pins it
	// (piSourceID): what a Pi's own packages are matched by
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// piListed is a package as the snapshot and the disk cache keep it.
type piListed struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Downloads   int    `json:"downloads,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Date        string `json:"date,omitempty"`
}

const piSearchPath = "/-/v1/search"

// In automatic mode Pi tries the effective registry, then npm's own if
// it can't answer. Variables for tests.
var (
	piNPMBase  = npmregistry.Default
	piRegistry = plugin.Registry
)

//go:embed market_pi.json
var piSnapshot []byte

var piPopular = struct {
	sync.Mutex
	upstream string // empty in automatic mode, the configured URL otherwise
	list     []piListed
	err      error
	until    time.Time
}{}

// popularPi is the most downloaded of the first 500 packages npm lists by
// the keyword: Pi's gallery's own source. The disk cache is only for
// automatic mode; a configured source keeps its own results in memory.
func popularPi(registry plugin.RegistryInUse) ([]piListed, error) {
	upstream := ""
	if registry.From == plugin.FromMagpie {
		upstream = registry.URL
	}
	piPopular.Lock()
	defer piPopular.Unlock()
	same := piPopular.upstream == upstream
	if same && time.Now().Before(piPopular.until) {
		return piPopular.list, piPopular.err
	}
	list, err := fetchPiPopular(registry)
	ttl := 6 * time.Hour
	if upstream == "" {
		cache := marketCache("pi-packages.json")
		if err == nil && len(list) > 0 {
			if b, marshalErr := json.Marshal(list); marshalErr == nil && cache != "" {
				_ = os.MkdirAll(filepath.Dir(cache), 0o755)
				_ = os.WriteFile(cache, b, 0o644)
			}
		} else {
			if b, readErr := os.ReadFile(cache); readErr != nil || json.Unmarshal(b, &list) != nil || len(list) == 0 {
				list = builtinPi()
			}
			ttl, err = 5*time.Minute, nil
		}
	} else if err != nil {
		if list == nil && same {
			list = piPopular.list
		}
		ttl = 5 * time.Minute
	}
	piPopular.upstream, piPopular.list, piPopular.err, piPopular.until = upstream, list, err, time.Now().Add(ttl)
	return list, err
}

var builtinPi = sync.OnceValue(func() []piListed {
	var list []piListed
	_ = json.Unmarshal(piSnapshot, &list)
	return list
})

func fetchPiPopular(registry plugin.RegistryInUse) ([]piListed, error) {
	var err error
	list := []piListed{}
	for from := 0; from < 500; from += 250 {
		page, total, pageErr := searchPiNPM(registry, "", 250, from)
		if pageErr != nil {
			if len(list) == 0 {
				return nil, pageErr
			}
			if registry.From == plugin.FromMagpie {
				err = pageErr
			}
			break
		}
		list = append(list, page...)
		if from+250 >= total {
			break
		}
	}
	slices.SortStableFunc(list, func(a, b piListed) int { return cmp.Compare(b.Downloads, a.Downloads) })
	return piOnce(list), err
}

// piRegistryJSON asks the selected registry. Only automatic mode tries
// npm's public registry as a fallback when the other source fails.
func piRegistryJSON[T any](registry plugin.RegistryInUse, name, path string) (T, error) {
	var doc T
	if registry.From != plugin.FromMagpie {
		if r := strings.TrimRight(registry.URL, "/"); r != "" && r != piNPMBase {
			var mirror T
			if err := getJSON(r+path, &mirror); err == nil {
				return mirror, nil
			}
		}
		err := getJSON(piNPMBase+path, &doc)
		return doc, err
	}
	req, err := http.NewRequest(http.MethodGet, piNPMBase+path, nil)
	if err != nil {
		return doc, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := npmregistry.Do(client, req, registry.URL, name)
	if err != nil {
		return doc, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("%s answered %s", res.Request.URL.Host, res.Status)
	}
	const limit = 8 << 20
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return doc, err
	}
	if len(b) > limit {
		return doc, fmt.Errorf("%s's npm registry response is too large", res.Request.URL.Host)
	}
	err = json.Unmarshal(b, &doc)
	return doc, err
}

// searchPiNPM is one page of npm's search for packages with the keyword,
// and how many there are.
func searchPiNPM(registry plugin.RegistryInUse, q string, size, from int) ([]piListed, int, error) {
	text := "keywords:pi-package"
	if q = strings.TrimSpace(q); q != "" {
		text += " " + q
	}
	type searchBody struct {
		Total   int `json:"total"`
		Objects []struct {
			Downloads struct {
				Monthly int `json:"monthly"`
			} `json:"downloads"`
			Package struct {
				Name        string   `json:"name"`
				Version     string   `json:"version"`
				Description string   `json:"description"`
				Keywords    []string `json:"keywords"`
				Date        string   `json:"date"`
				Publisher   struct {
					Username string `json:"username"`
				} `json:"publisher"`
				Links struct {
					Homepage   string `json:"homepage"`
					Repository string `json:"repository"`
				} `json:"links"`
			} `json:"package"`
		} `json:"objects"`
	}
	var body searchBody
	ask := func(text string) error {
		query := url.Values{"text": {text}, "size": {strconv.Itoa(size)}, "from": {strconv.Itoa(from)}}.Encode()
		doc, err := piRegistryJSON[searchBody](registry, "", piSearchPath+"?"+query)
		if err == nil && registry.From == plugin.FromMagpie && doc.Objects == nil {
			err = errors.New("npm registry search response has no objects array")
		}
		if err != nil {
			return &SearchError{Source: "npm", Err: err}
		}
		body = doc
		return nil
	}
	if err := ask(text); err != nil {
		return nil, 0, err
	}
	// npmmirror answers nothing to a keywords: qualifier, and finds a
	// search's words with the keyword as a plain word (PlainKeywords:
	// eachg0 on X, "rename" found no pi-rename). The list before a search
	// stays the one magpie shipped with there: npmmirror's has no monthly
	// downloads to put the most used first.
	if (q != "" || registry.From == plugin.FromMagpie) && len(body.Objects) == 0 && from == 0 {
		_ = ask(plugin.PlainKeywords(text)) // on error the first answer stands
	}
	var out []piListed
	for _, o := range body.Objects {
		p := o.Package
		// a word of the search can match a package by its text alone; Pi's
		// gallery lists only those with the keyword
		if !slices.Contains(p.Keywords, "pi-package") || !piNameRe.MatchString(p.Name) {
			continue
		}
		d := strings.TrimSpace(p.Description)
		if r := []rune(d); len(r) > 280 {
			d = strings.TrimSpace(string(r[:277])) + "…"
		}
		out = append(out, piListed{Name: p.Name, Version: p.Version, Description: d, Publisher: p.Publisher.Username,
			Downloads: o.Downloads.Monthly, Repository: p.Links.Repository, Homepage: p.Links.Homepage, Date: p.Date})
	}
	return out, body.Total, nil
}

func piOnce(list []piListed) []piListed {
	seen := map[string]bool{}
	return slices.DeleteFunc(list, func(p piListed) bool {
		if seen[p.Name] {
			return true
		}
		seen[p.Name] = true
		return false
	})
}

// MarketPiPackages is what the market shows for a search: the popular
// packages that match it, then what npm finds for it.
func MarketPiPackages(query string) ([]PiMarketPackage, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	registry := piRegistry()
	popular, err := popularPi(registry)
	var found []piListed
	if len([]rune(q)) >= 2 {
		found, _, err = searchPiNPM(registry, q, 100, 0)
	}
	// Use the shipped list only when the configured source couldn't answer.
	// Don't let it mask an exact lookup (PiDirectAt) for a named package.
	if registry.From == plugin.FromMagpie && popular == nil && err != nil {
		src, bare := piSourceFor(query)
		if src == "" || bare || piKind(src) != "npm" {
			popular = builtinPi()
			if bare && src != "" {
				name, _ := piPackageName(src)
				popular = slices.DeleteFunc(slices.Clone(popular), func(p piListed) bool { return p.Name == name })
			}
		}
	}
	var list []piListed
	for _, p := range popular {
		if q == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Publisher+" "+p.Description), q) {
			list = append(list, p)
		}
	}
	if len(found) > 0 {
		list = piOnce(append(list, found...))
	}
	out := []PiMarketPackage{}
	for _, p := range list {
		out = append(out, piMarketOf(p))
	}
	return out, err
}

func piMarketOf(p piListed) PiMarketPackage {
	m := PiMarketPackage{Source: "npm:" + p.Name, Name: p.Name, Version: p.Version, Description: p.Description,
		Publisher: p.Publisher, Downloads: p.Downloads, NPM: "https://www.npmjs.com/package/" + p.Name,
		Kind: "npm", ID: "npm:" + p.Name}
	if repo := piRepoURL(p.Repository); repo != "" {
		m.Repo = repo
		if owner := githubOwner(repo); owner != "" {
			m.Icon = gh(owner)
		}
	}
	if strings.HasPrefix(p.Homepage, "https://") {
		m.Homepage = p.Homepage
	}
	if len(p.Date) >= 10 {
		m.Updated = p.Date[:10]
	}
	return m
}

// piRepoURL is a package's repository as a page to open: npm's
// git+https://…/x.git as https://…/x; nothing that isn't https.
func piRepoURL(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "git+"), ".git")
	if !strings.HasPrefix(s, "https://") {
		return ""
	}
	return s
}

// ---- install and remove -----------------------------------------------------

// piNameRe is an npm package's name (validate-npm-package-name): never one
// that starts with "-" or ".", which a command would read as a flag or a
// path.
var piNameRe = lazyre.New(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9_][a-z0-9._~-]*$`)

// PiInstalled is what an install or a removal did, for the page to say.
type PiInstalled struct {
	*PiExtensions
	// Pending: only settings.json was changed; Pi installs the package
	// (or no longer loads it) when it next starts
	Pending bool `json:"pending,omitempty"`
}

// piBin is the pi command magpie runs for p, "" when it can't run one: a
// Pi in WSL, or one whose CLI isn't found; a var for tests.
var piBin = func(p piPlace) string {
	if p.a.WSL != "" {
		return ""
	}
	return p.a.BinPath()
}

// piRun runs `pi <args>` for the Pi whose agent folder is dir, with
// nothing to answer it; a var for tests.
var piRun = func(bin, dir string, args ...string) (string, error) {
	const timeout = 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Dir = dir
	// pi is a node script: its folder first, so its node is found
	cmd.Env = npmregistry.Env(append(os.Environ(), "PI_CODING_AGENT_DIR="+dir, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH")))
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("pi %s didn't finish in %s", strings.Join(args, " "), timeout)
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("pi %s: %s", strings.Join(args, " "), lastLines(text, 4))
	}
	return text, nil
}

// InstallPiPackage installs a package from npm or git into a Pi: one
// from the market, or one the search named (PiDirectAt).
func InstallPiPackage(pi, source string) (*PiInstalled, error) {
	source = strings.TrimSpace(source)
	if !piSourceOK(source) {
		return nil, fmt.Errorf("%q isn't a package Pi can install from npm or git", source)
	}
	name, _ := piPackageName(source)
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	id := p.piIdentity(source)
	for _, r := range s.pkgs {
		if x, ok := piPackageOf(r); ok && p.piIdentity(x.Source) == id {
			return nil, fmt.Errorf("%s has %s already", piLabel(p), name)
		}
	}
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if bin := piBin(p); bin != "" {
		if _, err := piRun(bin, p.dir, "install", source); err != nil {
			return nil, err
		}
		if has, err := p.has(id); err != nil {
			return nil, err
		} else if !has {
			return nil, fmt.Errorf("pi install %s finished, but %s's settings.json doesn't list it", source, piLabel(p))
		}
		return &PiInstalled{PiExtensions: ReadPiExtensions()}, nil
	}
	b, _ := json.Marshal(source)
	next := append(slices.Clone(s.pkgs), b)
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return nil, err
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return &PiInstalled{PiExtensions: ReadPiExtensions(), Pending: true}, nil
}

// RemovePiPackage takes a package out of a Pi, as `pi remove` does.
func RemovePiPackage(pi, source string) (*PiInstalled, error) {
	source = strings.TrimSpace(source)
	if source == "" || strings.HasPrefix(source, "-") {
		return nil, fmt.Errorf("%q isn't a package", source)
	}
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	id := p.piIdentity(source)
	var next []json.RawMessage
	for _, r := range s.pkgs {
		if x, ok := piPackageOf(r); ok && p.piIdentity(x.Source) == id {
			continue
		}
		next = append(next, r)
	}
	if len(next) == len(s.pkgs) {
		return nil, fmt.Errorf("%s has no package %s", piLabel(p), source)
	}
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if bin := piBin(p); bin != "" {
		if _, err := piRun(bin, p.dir, "remove", source); err != nil {
			return nil, err
		}
		if has, err := p.has(id); err != nil {
			return nil, err
		} else if has {
			return nil, fmt.Errorf("pi remove %s finished, but %s's settings.json still lists it", source, piLabel(p))
		}
		return &PiInstalled{PiExtensions: ReadPiExtensions()}, nil
	}
	if next == nil {
		next = []json.RawMessage{}
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return &PiInstalled{PiExtensions: ReadPiExtensions(), Pending: true}, nil
}

// has says whether p's settings.json lists a package of this identity;
// one that can't be read says nothing either way.
func (p piPlace) has(id string) (bool, error) {
	s, err := p.settings()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(s.pkgs, func(r json.RawMessage) bool {
		x, ok := piPackageOf(r)
		return ok && p.piIdentity(x.Source) == id
	}), nil
}

// ---- a package named outright -----------------------------------------------

// eachg0 on X: the skills and MCP searches take a repository or a package
// named outright, and Pi's only searched, so a package found elsewhere
// couldn't be installed from it. A search that names one the way `pi
// install` takes it is now offered as named, whatever the search finds.
// Pi 1.5.2 (core/package-manager.js parseSource, utils/git.js parseGitUrl)
// installs from:
//   - npm:<name>[@<version>]
//   - git:<host>/<path>[@<ref>], git:git@<host>:<path>[@<ref>], git:<URL>,
//     and hosted-git-info's git:<owner>/<repo> and git:github:<owner>/<repo>
//   - an https://, http://, ssh:// or git:// address of a repository
// and reads anything else as a folder. As the other searches do, magpie
// also takes what Pi would read as a folder but is plainly a package: a
// scoped name, git@<host>:<path>, <host.tld>/<owner>/<repo>, GitHub's
// <owner>/<repo>, a package's page on npmjs.com, a repository's page on
// GitHub (…/tree/<ref>, #readme), and a bare name the registry has with
// Pi's keyword (a bare word is a search first: "web" is a package too).
// What it installs is always one of Pi's own forms. A folder on this
// computer isn't offered: the market installs what can be fetched.

var (
	// piPinRe is an npm version or range, and piRefRe a git ref: never
	// one that starts with "-", which git or npm would read as a flag
	piPinRe = lazyre.New(`^[A-Za-z0-9._^~<>=*+][A-Za-z0-9._^~<>=*+-]*$`)
	piRefRe = lazyre.New(`^[A-Za-z0-9._+][A-Za-z0-9._+-]*$`)
	// piHostRe is a git host's name, and piPartRe one part of a path
	piHostRe = lazyre.New(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	piPartRe = lazyre.New(`^[A-Za-z0-9._][A-Za-z0-9._~-]*$`)
)

// piSourceOK says whether s is an npm or git source in one of Pi's own
// forms that InstallPiPackage may hand to pi install.
func piSourceOK(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "\\?#%") ||
		strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	switch piKind(s) {
	case "npm":
		name, pin := piPackageName(s)
		return piNameRe.MatchString(name) && (pin == "" || piPinRe.MatchString(pin)) &&
			!strings.HasSuffix(s, "@") // npm:x@ names no version
	case "git":
		rest := strings.TrimPrefix(s, "git:")
		if rest == "" || strings.HasPrefix(rest, "-") {
			return false
		}
		if i := strings.LastIndex(rest, "@"); i > 0 && !strings.Contains(rest[i:], "/") && !strings.Contains(rest[i:], ":") {
			if !piRefRe.MatchString(rest[i+1:]) {
				return false
			}
		}
		host, p := piGitPlace(s)
		parts := strings.Split(p, "/")
		if !piHostRe.MatchString(host) || len(parts) < 2 {
			return false
		}
		for _, x := range parts {
			if x == "." || x == ".." || !piPartRe.MatchString(x) {
				return false
			}
		}
		// Pi takes a host without git: only with a scheme, and a shorthand
		// host only with git: (parseGitUrl)
		return strings.HasPrefix(s, "git:") || strings.Contains(s, "://")
	}
	return false
}

// piHosted is hosted-git-info's shorthands as the hosts they name, so a
// package is one identity however it was typed.
var piHosted = map[string]string{"github": "github.com", "gitlab": "gitlab.com", "bitbucket": "bitbucket.org"}

// piSourceFor is the source a search names, in Pi's own form, or "" when
// it names none; bare: a name alone, which is offered only if the
// registry has it with Pi's keyword.
func piSourceFor(q string) (src string, bare bool) {
	q = strings.TrimSpace(q)
	low := strings.ToLower(q)
	plain := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(low, "https://"), "http://"), "www.")
	switch {
	case q == "":
		return "", false
	case strings.HasPrefix(low, "npm:"):
		src = "npm:" + strings.TrimSpace(q[4:])
	case strings.HasPrefix(plain, "npmjs.com/package/"), strings.HasPrefix(plain, "npmjs.org/package/"):
		src = piNPMPage(q[strings.Index(low, "/package/")+len("/package/"):])
	case strings.HasPrefix(low, "git:"):
		src = "git:" + piShorthand(strings.TrimSpace(q[4:]))
	case strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "ssh://"), strings.HasPrefix(low, "git://"):
		src = piRepoPage(q)
	case strings.HasPrefix(q, "git@"):
		src = "git:" + q
	case strings.HasPrefix(q, "@"):
		src = "npm:" + q
	case strings.Contains(q, "/"):
		if host, _, _ := strings.Cut(q, "/"); strings.Contains(host, ".") {
			src = "git:" + q
		} else if strings.Count(q, "/") == 1 {
			src = "git:github.com/" + q
		}
	default:
		src, bare = "npm:"+q, true
	}
	if !piSourceOK(src) {
		return "", false
	}
	return src, bare
}

// piShorthand is git:'s text with a hosted shorthand (github:o/r, o/r)
// written out as its host.
func piShorthand(s string) string {
	if k, rest, ok := strings.Cut(s, ":"); ok && piHosted[strings.ToLower(k)] != "" {
		return piHosted[strings.ToLower(k)] + "/" + rest
	}
	if host, _, ok := strings.Cut(s, "/"); ok && !strings.Contains(host, ".") && !strings.Contains(host, ":") && !strings.Contains(host, "@") && strings.Count(s, "/") == 1 {
		return "github.com/" + s
	}
	return s
}

// piNPMPage is npm's page of a package (…/package/<name>[/v/<version>])
// as the package.
func piNPMPage(rest string) string {
	rest, _, _ = strings.Cut(rest, "?")
	rest, _, _ = strings.Cut(rest, "#")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	n := 1
	if strings.HasPrefix(parts[0], "@") {
		n = 2
	}
	if len(parts) < n {
		return ""
	}
	name := strings.Join(parts[:n], "/")
	if len(parts) >= n+2 && parts[n] == "v" {
		return "npm:" + name + "@" + parts[n+1]
	}
	return "npm:" + name
}

// piRepoPage is a repository's address as Pi takes it: a page of one on
// GitHub, GitLab or Bitbucket as the repository (…/tree/<ref> as its ref,
// #readme and what follows the repository gone); any other as it is.
func piRepoPage(q string) string {
	u, err := url.Parse(q)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if (u.Scheme != "https" && u.Scheme != "http") || !slices.Contains([]string{"github.com", "gitlab.com", "bitbucket.org"}, host) {
		return strings.TrimRight(q, "/")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	src := "https://" + host + "/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	rest := parts[2:]
	if len(rest) > 0 && rest[0] == "-" { // GitLab's /-/tree/<ref>
		rest = rest[1:]
	}
	if len(rest) >= 2 && (rest[0] == "tree" || rest[0] == "src") {
		src += "@" + rest[1]
	}
	return src
}

// piLatest is the selected registry's latest of an npm package.
func piLatest(registry plugin.RegistryInUse, name string) (piListed, []string, error) {
	type packageDoc struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Keywords    []string        `json:"keywords"`
		Homepage    string          `json:"homepage"`
		Repository  json.RawMessage `json:"repository"`
		NPMUser     struct {
			Name string `json:"name"`
		} `json:"_npmUser"`
	}
	doc, err := piRegistryJSON[packageDoc](registry, name, "/"+url.PathEscape(name)+"/latest")
	if err != nil {
		return piListed{}, nil, err
	}
	if doc.Name != name {
		return piListed{}, nil, fmt.Errorf("the registry answered %q for %s", doc.Name, name)
	}
	repo := ""
	if json.Unmarshal(doc.Repository, &repo) != nil {
		var r struct{ URL string }
		json.Unmarshal(doc.Repository, &r)
		repo = r.URL
	}
	d := strings.TrimSpace(doc.Description)
	if r := []rune(d); len(r) > 280 {
		d = strings.TrimSpace(string(r[:277])) + "…"
	}
	return piListed{Name: doc.Name, Version: doc.Version, Description: d, Publisher: doc.NPMUser.Name,
		Repository: repo, Homepage: doc.Homepage}, doc.Keywords, nil
}

// PiDirectAt is the package a search names outright, to install as named,
// when it's one Pi can install and none of what was found is it; nil
// otherwise.
func PiDirectAt(q string, found []PiMarketPackage) *PiMarketPackage {
	src, bare := piSourceFor(q)
	if src == "" {
		return nil
	}
	id := piSourceID(src)
	name, pin := piPackageName(src)
	if slices.ContainsFunc(found, func(m PiMarketPackage) bool { return m.Source == src || pin == "" && m.ID == id }) {
		return nil
	}
	if piKind(src) == "npm" {
		got, kw, err := piLatest(piRegistry(), name)
		if bare && (err != nil || !slices.Contains(kw, "pi-package")) {
			return nil
		}
		if err != nil {
			got = piListed{Name: name}
		}
		m := piMarketOf(got)
		m.Source = src
		if pin != "" {
			m.Version = pin
		}
		return &m
	}
	host, p := piGitPlace(src)
	m := PiMarketPackage{Source: src, Name: name, Kind: "git", ID: id, Publisher: strings.TrimPrefix(strings.TrimPrefix(src, "git:"), "https://")}
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "ssh://") && !strings.HasPrefix(src, "git://") && !strings.Contains(src, "git@") {
		m.Repo = "https://" + host + "/" + p
		if strings.EqualFold(host, "github.com") {
			m.Icon = gh(strings.Split(p, "/")[0])
		}
	}
	return &m
}
