package update

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/npmregistry"
)

// ExitError is a command RunElevated ran that ended with a code other
// than 0; winget's are HRESULTs (0x8A150019).
type ExitError struct{ Code uint32 }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// elevatedScript is the batch file RunElevated has cmd.exe run: the
// variables of env that differ from own's, PATH, and a package manager's
// registry are set; argv runs in dir with its output sent to log.
func elevatedScript(dir string, env, own []string, log string, argv []string) (string, error) {
	var b strings.Builder
	b.WriteString("@echo off\r\nsetlocal DisableDelayedExpansion\r\nchcp 65001 >nul\r\n")
	have := map[string]string{}
	for _, kv := range own {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			have[strings.ToUpper(k)] = v
		}
	}
	packageManager := len(argv) > 0 && npmregistry.Program(argv[0])
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		// "=C:" and its like are cmd.exe's own; a quote or a line break
		// can't be set from a batch file
		registry := packageManager && npmregistry.IsVar(k)
		if !ok || k == "" || strings.ContainsAny(k+v, "\"\r\n") {
			if ok && registry {
				return "", fmt.Errorf("cannot pass %s to the elevated package manager", k)
			}
			continue
		}
		if old, ok := have[strings.ToUpper(k)]; ok && old == v && !strings.EqualFold(k, "PATH") && !registry {
			continue
		}
		line := `set "` + batchEscape(k) + "=" + batchEscape(v) + "\"\r\n"
		if len(line) > 8000 { // cmd.exe's line is at most 8191 characters
			if registry {
				return "", fmt.Errorf("cannot pass %s to the elevated package manager: value is too long", k)
			}
			continue
		}
		b.WriteString(line)
	}
	if dir != "" {
		b.WriteString(`cd /d "` + batchEscape(dir) + "\"\r\n")
	}
	b.WriteString("call")
	for _, a := range argv {
		// call reads a line's % twice, so none is passed at all
		if strings.ContainsAny(a, "\"\r\n%") {
			return "", fmt.Errorf("can't pass %q to cmd.exe", a)
		}
		b.WriteString(` "` + a + `"`)
	}
	b.WriteString(` > "` + batchEscape(log) + "\" 2>&1\r\nexit /b %errorlevel%\r\n")
	return b.String(), nil
}

// batchEscape keeps a % in a batch file's line from being read as a
// variable.
func batchEscape(s string) string { return strings.ReplaceAll(s, "%", "%%") }
