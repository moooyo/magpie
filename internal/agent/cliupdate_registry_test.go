package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	syncatomic "sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func setCLIRegistryForTest(t *testing.T, registry string) {
	t.Helper()
	s := settings.Load()
	s.NPMRegistry = registry
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

func fakeGeminiWithNPM(t *testing.T) (a *Agent, manifest string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const pkg = "@google/gemini-cli"
	prefix := filepath.Join(t.TempDir(), "npm")
	bin, modules := filepath.Join(prefix, "bin"), filepath.Join(prefix, "lib", "node_modules")
	if runtime.GOOS == "windows" {
		bin, modules = prefix, filepath.Join(prefix, "node_modules")
		file(t, filepath.Join(bin, "gemini.cmd"), "@rem /node_modules/"+pkg+"/bundle/gemini.js\r\n@exit /b 0\r\n")
		file(t, filepath.Join(bin, "npm.cmd"), "@exit /b 1\r\n")
	} else {
		file(t, filepath.Join(modules, pkg, "bundle", "gemini.js"), "#!/bin/sh\nexit 0\n")
		link(t, "../lib/node_modules/"+pkg+"/bundle/gemini.js", filepath.Join(bin, "gemini"))
		file(t, filepath.Join(bin, "npm"), "#!/bin/sh\nexit 1\n")
	}
	manifest = file(t, filepath.Join(modules, pkg, "package.json"), `{"version":"0.60.0"}`)
	t.Setenv("PATH", bin)
	return &Agent{ID: "gemini", Name: "Gemini CLI", Bin: "gemini"}, manifest
}

func TestCLILatestFollowsRegistry(t *testing.T) {
	a, _ := fakeGeminiWithNPM(t)
	var defaults syncatomic.Int32
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/@google/gemini-cli/latest":
			defaults.Add(1)
			w.Write([]byte(`{"version":"0.61.0"}`))
		case "/repo/npm/@google/gemini-cli/latest":
			w.Write([]byte(`{"version":"0.63.0"}`))
		case "/down/@google/gemini-cli/latest":
			http.Error(w, "unavailable", http.StatusBadGateway)
		default:
			t.Errorf("unexpected npm path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(reg.Close)
	old := npmRegistry
	npmRegistry = reg.URL + "/"
	latests = memo{}
	t.Cleanup(func() { npmRegistry = old; latests = memo{} })

	for i, c := range []struct{ registry, want string }{
		{"", "0.61.0"},
		{reg.URL + "/repo/npm/", "0.63.0"},
		{reg.URL + "/down", ""},
		{"", "0.61.0"},
	} {
		setCLIRegistryForTest(t, c.registry)
		cli, ok := a.CLI()
		if !ok || cli.Version != "0.60.0" || cli.Latest != c.want || cli.Update != (c.want != "") {
			t.Fatalf("registry %q: %+v; want latest %q", c.registry, cli, c.want)
		}
		want := int32(1)
		if i == 3 {
			want++
		}
		if got := defaults.Load(); got != want {
			t.Fatalf("registry %q: default contacted %d times, want %d", c.registry, got, want)
		}
	}
}

func wantCLIRegistryVar(t *testing.T, env []string, key, want string) {
	t.Helper()
	var got []string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s: got %q, want one %q", key, got, want)
	}
}

func TestCLIUpdateRegistryEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const configured = "https://registry.example/repo"
	const inherited = "https://inherited.example/"
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "NPM_CONFIG_REGISTRY") || strings.EqualFold(k, "BUN_CONFIG_REGISTRY") {
			t.Setenv(k, "")
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("npm_config_registry", inherited)
	t.Setenv("BUN_CONFIG_REGISTRY", inherited)
	omo := howInstalled(cliSpecs["omo"], "/opt/bin/omo")
	standalone := howInstalled(cliSpecs["opencode"], "/home/me/.opencode/bin/opencode")
	if omo == nil || standalone == nil {
		t.Fatal("missing self updater")
	}
	for _, c := range []struct {
		name string
		u    updater
		want string
	}{
		{"npm shim", updater{via: "npm", cmd: []string{`C:\Program Files\nodejs\npm.cmd`}}, configured + "/"},
		{"pnpm", updater{via: "pnpm", cmd: []string{"/usr/bin/pnpm"}}, configured + "/"},
		{"bun", updater{via: "bun", cmd: []string{"bun"}}, configured + "/"},
		{"OmO", *omo, configured + "/"},
		{"standalone", *standalone, inherited},
		{"brew", updater{via: "brew", cmd: []string{"brew"}}, inherited},
		{"reshim", updater{via: "reshim", cmd: []string{"mise", "reshim"}}, inherited},
		{"unset", updater{via: "npm", cmd: []string{"npm"}}, inherited},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name != "unset" {
				c.u.registry = configured
			}
			env := updateEnv(&c.u)
			wantCLIRegistryVar(t, env, "NPM_CONFIG_REGISTRY", c.want)
			wantCLIRegistryVar(t, env, "BUN_CONFIG_REGISTRY", c.want)
		})
	}
}

func TestCLIUpdateRefusesOlderRegistryVersion(t *testing.T) {
	a, _ := fakeGeminiWithNPM(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.59.0"}`))
	}))
	t.Cleanup(s.Close)
	setCLIRegistryForTest(t, s.URL)
	oldRun, oldAdmin := runUpdate, runAdmin
	refuse := func(context.Context, *updater) ([]byte, error) {
		t.Error("started a package manager to install an older version")
		return nil, nil
	}
	runUpdate, runAdmin = refuse, refuse
	t.Cleanup(func() { runUpdate, runAdmin = oldRun, oldAdmin; latests = memo{} })
	if _, err := a.UpdateCLI(); err == nil || !strings.Contains(err.Error(), "older than the installed 0.60.0") {
		t.Fatalf("update against an older mirror: %v", err)
	}
}

func TestCLIUpdateKeepsRegistrySnapshot(t *testing.T) {
	a, manifest := fakeGeminiWithNPM(t)
	var paths []string
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/before/@google/gemini-cli/latest" {
			t.Errorf("looked up the running update at %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"version":"0.61.0"}`))
	}))
	t.Cleanup(reg.Close)
	oldReg, oldRun, oldAdmin, oldWrite, oldNPM := npmRegistry, runUpdate, runAdmin, canWrite, adminNPM
	t.Cleanup(func() {
		npmRegistry, runUpdate, runAdmin, canWrite, adminNPM = oldReg, oldRun, oldAdmin, oldWrite, oldNPM
		versions, latests = memo{}, memo{}
	})
	npmRegistry = reg.URL + "/"
	latests = memo{}
	const installed = `{"version":"0.61.0"}`
	setCLIRegistryForTest(t, reg.URL+"/before")
	// A prefix requiring administrator access takes the elevated runner,
	// which gets its environment from the same update as a normal runner.
	canWrite, adminNPM = func(string) bool { return false }, true
	runUpdate = func(context.Context, *updater) ([]byte, error) {
		t.Fatal("a protected prefix ran the unelevated updater")
		return nil, nil
	}
	runAdmin = func(_ context.Context, u *updater) ([]byte, error) {
		paths = append(paths, u.cmd[0])
		setCLIRegistryForTest(t, reg.URL+"/after")
		env := updateEnv(u)
		wantCLIRegistryVar(t, env, "NPM_CONFIG_REGISTRY", reg.URL+"/before/")
		wantCLIRegistryVar(t, env, "BUN_CONFIG_REGISTRY", reg.URL+"/before/")
		file(t, manifest, installed)
		return nil, nil
	}
	got, err := a.UpdateCLI()
	if err != nil || len(paths) != 1 || got.Version != "0.61.0" || got.Latest != "0.61.0" || got.Update {
		t.Fatalf("update: %+v, %v; executed %q", got, err, paths)
	}
}
