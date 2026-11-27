package library

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// libraryNPMProbe reports what a managed child receives without running
// an actual agent or package manager.
func libraryNPMProbe(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		write(t, bin, "@echo off\r\necho(%NPM_CONFIG_REGISTRY%\r\necho(%BUN_CONFIG_REGISTRY%\r\necho(%npm_config_registry%\r\necho(%MAGPIE_TEST_NPM_OTHER%\r\necho(%~1 %~2 %~3 %~4 %~5\r\n")
	} else {
		testenv.Program(t, bin, `#!/bin/sh
printf '%s\n' "$NPM_CONFIG_REGISTRY" "$BUN_CONFIG_REGISTRY" "$npm_config_registry" "$MAGPIE_TEST_NPM_OTHER" "$*"
`)
	}
	return bin
}

func TestLibraryManagedRegistryEnv(t *testing.T) {
	h := sandbox(t)
	const upstream = "https://packages.example.test/npm/"
	const inherited = "https://inherited.example.test/npm/"
	t.Setenv("NPM_CONFIG_REGISTRY", inherited)
	t.Setenv("BUN_CONFIG_REGISTRY", inherited)
	t.Setenv("MAGPIE_TEST_NPM_OTHER", "preserved")
	if runtime.GOOS != "windows" {
		t.Setenv("npm_config_registry", inherited)
	}
	setLibraryNPMUpstream(t, upstream)
	pi := libraryNPMProbe(t, "pi")
	dsh := libraryNPMProbe(t, "dsh")
	bili := libraryNPMProbe(t, "bili")

	lower := ""
	if runtime.GOOS == "windows" {
		lower = upstream // env keys are case-insensitive here
	}
	wantEnv := strings.Join([]string{upstream, upstream, lower, "preserved"}, "\n")
	check := func(want, out string, err error) {
		t.Helper()
		if got := strings.ReplaceAll(out, "\r\n", "\n"); err != nil || got != want {
			t.Errorf("child printed %q, err %v; want %q", got, err, want)
		}
	}
	for _, verb := range []string{"install", "remove"} {
		out, err := piRun(pi, h, verb, "npm:pi-tools")
		check(wantEnv+"\n"+verb+" npm:pi-tools", out, err)
		out, err = biliRun(bili, nil, time.Minute, "plugin", verb, "pi")
		check(wantEnv+"\nplugin "+verb+" pi", out, err)
	}
	out, err := dshPluginRun(dsh, h, h, upstream, "plugin", "--profile", "web", "add", "example")
	check(wantEnv+"\nplugin --profile web add example", out, err)

	wantInherited := strings.Join([]string{inherited, inherited, inherited, "preserved"}, "\n")
	out, err = biliRun(bili, nil, time.Minute, "plugin", "list")
	check(wantInherited+"\nplugin list", out, err)
	if os.Getenv("NPM_CONFIG_REGISTRY") != inherited || os.Getenv("BUN_CONFIG_REGISTRY") != inherited {
		t.Error("the parent environment changed")
	}
	setLibraryNPMUpstream(t, "")
	out, err = piRun(pi, h, "install", "npm:pi-tools")
	check(wantInherited+"\ninstall npm:pi-tools", out, err)
}

func TestInstallBiliPassesRegistryOnlyToItsNPMCommand(t *testing.T) {
	sandbox(t)
	bin := libraryNPMProbe(t, "npm")
	path, installer, run := biliPath, biliInstaller, runPlain
	biliPath = func() string { return "" }
	biliInstaller = func() []string { return []string{bin, "install", "-g", biliPackage} }
	t.Cleanup(func() { biliPath, biliInstaller, runPlain = path, installer, run })
	stop := errors.New("captured npm invocation")
	var args string
	runPlain = func(c []string, timeout time.Duration, env ...string) (string, error) {
		out, err := run(c, timeout, env...)
		if err != nil {
			return out, err
		}
		args = lastLines(strings.TrimSpace(out), 1)
		return out, stop
	}

	const command = "install -g billion-context"
	if _, err := InstallBili(); !errors.Is(err, stop) || args != command {
		t.Errorf("automatic install = %q, %v; want %q", args, err, command)
	}
	const upstream = "https://packages.example.test/npm"
	setLibraryNPMUpstream(t, upstream)
	if _, err := InstallBili(); !errors.Is(err, stop) || args != command+" --registry="+upstream {
		t.Errorf("configured install = %q, %v", args, err)
	}
}
