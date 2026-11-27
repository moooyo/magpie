package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// opencodeRegistry serves the newest of npm's two OpenCode packages as
// npm did on 2026-11-05: @opencode/cli is OpenCode 2, opencode-ai
// OpenCode 1 only.
func opencodeRegistry(t *testing.T, configured bool) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prefix := ""
	if configured {
		prefix = "/mirror"
	}
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "/@opencode/cli/latest":
			w.Write([]byte(`{"name":"@opencode/cli","version":"2.2.5"}`))
		case prefix + "/opencode-ai/latest":
			w.Write([]byte(`{"name":"opencode-ai","version":"1.19.5"}`))
		default:
			t.Errorf("unexpected version lookup at %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	oldReg := npmRegistry
	npmRegistry = reg.URL + "/"
	versions, latests, wslProbes = memo{}, memo{}, memo{}
	t.Cleanup(func() { reg.Close(); npmRegistry = oldReg; versions, latests, wslProbes = memo{}, memo{}, memo{} })
	if configured {
		setCLIRegistryForTest(t, reg.URL+prefix)
	}
}

// An OpenCode from its installer, in ~/.opencode/bin (OpenCode 2's
// opencode.ai/v2/install puts it there as OpenCode 1's did), is offered
// the newest of its own line and upgraded to it: OpenCode 2 to
// @opencode/cli's 2.2.5, OpenCode 1 to opencode-ai's 1.19.5, never to a
// 2.x, which OpenCode 1's installer refuses ("is not an OpenCode 1
// release, so this installer can't install it").
func TestUpdateCLIOpenCodeKeepsItsLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as the CLI")
	}
	for _, c := range []struct {
		name, has, latest string
		configured        bool
	}{
		{"automatic 2.x", "2.2.4", "2.2.5", false},
		{"automatic 1.x", "1.19.4", "1.19.5", false},
		{"configured 2.x", "2.2.4", "2.2.5", true},
		{"configured 1.x", "1.19.4", "1.19.5", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tmp, _ := filepath.EvalSymlinks(t.TempDir())
			verFile := filepath.Join(tmp, "version")
			os.WriteFile(verFile, []byte("opencode v"+c.has+"\n"), 0o644)
			oc := file(t, filepath.Join(tmp, "home/.opencode/bin/opencode"), "#!/bin/sh\n/bin/cat "+verFile+"\n")
			bin := link(t, oc, filepath.Join(tmp, "bin/opencode"))
			t.Setenv("PATH", filepath.Dir(bin))
			opencodeRegistry(t, c.configured)
			oldRun := runUpdate
			var ran [][]string
			runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
				ran = append(ran, u.cmd)
				os.WriteFile(verFile, []byte("opencode v"+c.latest+"\n"), 0o644)
				return nil, nil
			}
			t.Cleanup(func() { runUpdate = oldRun })

			a := &Agent{ID: "opencode", Name: "OpenCode", Bin: "opencode"}
			if got, ok := a.CLI(); !ok || got.Via != "self" || got.Version != c.has || got.Latest != c.latest || !got.Update {
				t.Fatalf("before: %+v", got)
			}
			if got, err := a.UpdateCLI(); err != nil || got.Version != c.latest || got.Update {
				t.Fatalf("after: %+v, %v", got, err)
			}
			if len(ran) != 1 || !reflect.DeepEqual(ran[0], []string{bin, "upgrade", c.latest}) {
				t.Errorf("ran %q", ran)
			}
		})
	}
}

func TestWSLOpenCodeKeepsItsLineWithConfiguredRegistry(t *testing.T) {
	for _, c := range []struct{ has, latest string }{{"1.19.4", "1.19.5"}, {"2.2.4", "2.2.5"}} {
		t.Run(c.has, func(t *testing.T) {
			opencodeRegistry(t, true)
			const bin = "/home/me/.opencode/bin/opencode"
			fakeWSL(t, "Ubuntu\n", "Ubuntu\n", map[string]string{
				"Ubuntu": "home:/home/me\ndir:.config/opencode\nbin:opencode " + bin + "\n",
			}, map[string]string{"Ubuntu": t.TempDir()})
			version := c.has
			probe := wslRun
			wslRun = func(timeout time.Duration, args ...string) ([]byte, error) {
				if len(args) > 5 && args[3] == "sh" && args[4] == "-c" && strings.Contains(args[5], "readlink") {
					return []byte("bin:" + bin + "\nreal:" + bin + "\ncode:0\nver:opencode " + version + "\n"), nil
				}
				return probe(timeout, args...)
			}
			old := wslRunUpdate
			t.Cleanup(func() { wslRunUpdate = old })
			var ran []string
			wslRunUpdate = func(_ time.Duration, u *updater) ([]byte, error) {
				ran = slices.Clone(u.cmd)
				version = c.latest
				return nil, nil
			}
			a := &Agent{ID: "opencode@wsl:Ubuntu", WSL: "Ubuntu", Name: "OpenCode"}
			if got, ok := a.CLI(); !ok || got.Via != "self" || got.Latest != c.latest || !got.Update {
				t.Errorf("before: %+v, %v", got, ok)
			}
			if got, err := a.UpdateCLI(); err != nil || got.Version != c.latest || got.Update {
				t.Errorf("after: %+v, %v", got, err)
			}
			if want := []string{bin, "upgrade", c.latest}; !slices.Equal(ran, want) {
				t.Errorf("ran %q, want %q", ran, want)
			}
		})
	}
}

// OpenCode 2 from npm (@opencode/cli), from bun and from anomalyco/tap's
// opencode-v2 formula is updated the way it was installed, its
// postinstall let run; an OpenCode 1 from npm stays opencode-ai's.
func TestHowInstalledOpenCode2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	spec := cliSpecs["opencode"]

	prefix := filepath.Join(tmp, "node")
	npm := file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\n")
	file(t, filepath.Join(prefix, "lib/node_modules/@opencode/cli/bin/opencode.exe"), "#!/bin/sh\n")
	oc := link(t, "../lib/node_modules/@opencode/cli/bin/opencode.exe", filepath.Join(prefix, "bin/opencode"))
	if u := howInstalled(spec, oc); u == nil || u.via != "npm" || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", prefix, "--allow-scripts=@opencode/cli", "@opencode/cli@latest"}) {
		t.Errorf("npm's @opencode/cli: %+v", u)
	}
	// its second command, opencode2, is the same program
	oc2 := link(t, "../lib/node_modules/@opencode/cli/bin/opencode.exe", filepath.Join(prefix, "bin/opencode2"))
	if u := howInstalled(spec, oc2); u == nil || u.pkg != "@opencode/cli" {
		t.Errorf("opencode2: %+v", u)
	}

	old := filepath.Join(tmp, "old")
	oldNpm := file(t, filepath.Join(old, "bin/npm"), "#!/bin/sh\n")
	file(t, filepath.Join(old, "lib/node_modules/opencode-ai/bin/opencode.exe"), "#!/bin/sh\n")
	oc1 := link(t, "../lib/node_modules/opencode-ai/bin/opencode.exe", filepath.Join(old, "bin/opencode"))
	if u := howInstalled(spec, oc1); u == nil || !reflect.DeepEqual(u.cmd, []string{oldNpm, "install", "-g", "--prefix", old, "--allow-scripts=opencode-ai", "opencode-ai@latest"}) {
		t.Errorf("npm's opencode-ai: %+v", u)
	}

	home := filepath.Join(tmp, "home")
	bun := file(t, filepath.Join(home, ".bun/bin/bun"), "")
	file(t, filepath.Join(home, ".bun/install/global/node_modules/@opencode/cli/bin/opencode.exe"), "")
	ocBun := link(t, "../install/global/node_modules/@opencode/cli/bin/opencode.exe", filepath.Join(home, ".bun/bin/opencode"))
	if u := howInstalled(spec, ocBun); u == nil || !reflect.DeepEqual(u.cmd, []string{bun, "add", "-g", "--trust", "@opencode/cli@latest"}) {
		t.Errorf("bun's @opencode/cli: %+v", u)
	}

	brewDir := filepath.Join(tmp, "homebrew")
	brew := file(t, filepath.Join(brewDir, "bin/brew"), "#!/bin/sh\n")
	file(t, filepath.Join(brewDir, "Cellar/opencode-v2/2.2.5/bin/opencode"), "")
	ocTap := link(t, "../Cellar/opencode-v2/2.2.5/bin/opencode", filepath.Join(brewDir, "bin/opencode"))
	if u := howInstalled(spec, ocTap); u == nil || !reflect.DeepEqual(u.cmd, []string{brew, "upgrade", "opencode-v2"}) {
		t.Errorf("anomalyco/tap's opencode-v2: %+v", u)
	}
}
