package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// An explicit upstream is not a fallback. Neither search terms nor a
// package name for download counts should leave the configured registry.
func TestGlobalRegistryDoesNotAskPublicNPM(t *testing.T) {
	registryHome(t)
	t.Setenv("MAGPIE_MIRRORS", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(ReloadInfo)
	const name = "opencode-npm-upstream-test"
	var public, stats atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		public.Add(1)
		fmt.Fprint(w, `{"objects":[]}`)
	}))
	t.Cleanup(origin.Close)
	downloads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.Add(1)
		fmt.Fprint(w, `{"downloads":12}`)
	}))
	t.Cleanup(downloads.Close)
	mirror := func(v int) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/repo/"+name+"/latest" {
				fmt.Fprintf(w, `{"version":"%d.0.0"}`, v)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(s.Close)
		return s
	}
	first, second := mirror(1), mirror(2)
	was, wasDL := npmRegistry, npmDownloads
	npmRegistry, npmDownloads = origin.URL, downloads.URL
	t.Cleanup(func() { npmRegistry, npmDownloads = was, wasDL })
	if _, err := SetRegistry(first.URL + "/repo/"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Search(ctx, "private query"); err == nil {
		t.Error("a missing search endpoint silently fell back to npm")
	}
	if got := Info(ctx, []string{name})[name]; got.Version != "1.0.0" || got.Weekly != 0 {
		t.Errorf("first upstream got %+v, want version 1.0.0 and no public counts", got)
	}
	if _, err := SetRegistry(second.URL + "/repo"); err != nil {
		t.Fatal(err)
	}
	if got := Info(ctx, []string{name})[name]; got.Version != "2.0.0" {
		t.Errorf("second upstream got %+v, want version 2.0.0, not a cached answer", got)
	}
	if public.Load() != 0 || stats.Load() != 0 {
		t.Errorf("explicit upstream leaked %d requests to the public registry and %d to public stats", public.Load(), stats.Load())
	}
	if _, err := SetRegistry("default"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUN_CONFIG_REGISTRY", first.URL+"/repo")
	if _, err := Search(ctx, "public query"); err != nil {
		t.Errorf("automatic search fallback no longer works: %v", err)
	}
	if got := Info(ctx, []string{name})[name]; got.Version != "1.0.0" || got.Weekly != 12 {
		t.Errorf("automatic upstream got %+v, want version 1.0.0 and public counts", got)
	}
	if public.Load() == 0 || stats.Load() != 1 {
		t.Errorf("automatic mode sent %d public registry and %d stats requests", public.Load(), stats.Load())
	}
}

func TestHostKeepsTheUpstreamItStartsWith(t *testing.T) {
	sandbox(t)
	writeFile(t, catalog.CachePath(), "{}")
	const first, second = "https://first.example/npm", "https://second.example/npm"
	if _, err := SetRegistry(first); err != nil {
		t.Fatal(err)
	}
	// A different process can save a new upstream between setting up the
	// host and building its Bun command.
	switched := false
	was := bunCommand
	t.Cleanup(func() { bunCommand = was })
	bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
		if !switched {
			switched = true
			if _, err := SetRegistry(second); err != nil {
				t.Fatal(err)
			}
		}
		return was(ctx, bun, dir, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h, _, err := startOn(ctx, os.Getenv("MAGPIE_BUN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.stop)
	if !switched || h.upstream != first || !slices.Contains(h.cmd.Env, "BUN_CONFIG_REGISTRY="+first+"/") {
		t.Fatalf("host remembered %q but its Bun registry does not match the startup snapshot", h.upstream)
	}
}
