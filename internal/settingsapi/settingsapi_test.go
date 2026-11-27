package settingsapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// home gives the test a home of its own, with no settings.json yet.
func home(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	testenv.SetHome(t, root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("APPDATA", filepath.Join(root, "AppData", "Roaming"))
}

// sample is a value of t that isn't was, as JSON carries it.
func sample(t reflect.Type, key string, was reflect.Value) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.Bool:
		v.SetBool(!was.Bool())
	case reflect.String:
		s := "x"
		switch key {
		case "vision", "imageGen", "searcher", "searcherThen":
			s = "off" // a model is checked against the catalog
		case "videoGen":
			s = "studio/clip-1" // a video model, videoModel's
		case "npmRegistry":
			s = "https://npm.example/repo"
		}
		for _, c := range Choices[key] {
			if c != was.String() {
				s = c
				break
			}
		}
		v.SetString(s)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(was.Int() + 7)
	case reflect.Float64, reflect.Float32:
		v.SetFloat(was.Float() + 1.5)
	case reflect.Pointer:
		elem := reflect.Zero(t.Elem())
		if !was.IsNil() {
			elem = was.Elem()
		}
		v.Set(reflect.New(t.Elem()))
		v.Elem().Set(sample(t.Elem(), key, elem))
	case reflect.Slice:
		el := sample(t.Elem(), key, reflect.Zero(t.Elem()))
		v.Set(reflect.Append(reflect.MakeSlice(t, 0, 1), el))
	case reflect.Map:
		v.Set(reflect.MakeMap(t))
		v.SetMapIndex(sample(t.Key(), "", reflect.Zero(t.Key())), sample(t.Elem(), "", reflect.Zero(t.Elem())))
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				v.Field(i).Set(sample(f.Type, "", reflect.Zero(f.Type)))
			}
		}
	}
	return v
}

// Keys are the fields POST /api/settings saves from what it's sent, no
// more and no fewer: each field of settings.Settings is sent changed, on
// its own, and the ones the save takes are compared with Keys. A field
// added to Settings that the save takes (or carries) without Keys knowing
// fails here, and so `magpie settings` can't fall behind the window
// (#4745).
func TestKeysAreWhatTheSaveWrites(t *testing.T) {
	home(t)
	videoModel(t)
	cur := settings.Defaults()
	curJSON, err := json.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	st := reflect.TypeFor[settings.Settings]()
	var written []string
	for i := range st.NumField() {
		f := st.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		was := reflect.ValueOf(cur).Field(i)
		val := sample(f.Type, key, was)
		b, err := json.Marshal(val.Interface())
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(curJSON, &m)
		m[key] = b
		body, _ := json.Marshal(m)
		in, err := Merge(body, cur, false)
		if err != nil {
			t.Fatalf("%s sent as %s: %v", key, b, err)
		}
		if !reflect.DeepEqual(reflect.ValueOf(in).Field(i).Interface(), was.Interface()) {
			written = append(written, key)
		}
	}
	// settings.Save keeps the file's own: the page can send it, and it
	// is never written by this save (SetSessionReads writes it)
	written = slices.DeleteFunc(written, func(k string) bool { return k == "sessionReads" })
	got, want := slices.Sorted(slices.Values(written)), slices.Sorted(slices.Values(Keys))
	if !slices.Equal(got, want) {
		t.Errorf("the save writes keys Keys doesn't name, or Keys names keys it doesn't write:\n only saved: %v\n only in Keys: %v",
			minus(got, want), minus(want, got))
	}
	for _, k := range Keys {
		if _, ok := Field(k); !ok {
			t.Errorf("Keys names %q, which settings.Settings doesn't have", k)
		}
	}
	if len(Keys) != len(slices.Compact(slices.Sorted(slices.Values(Keys)))) {
		t.Error("Keys names a key twice")
	}
}

// videoModel sets up a provider whose clip-1 the user marked a video
// model (#1399), so studio/clip-1 is one videoGen can name.
func videoModel(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "studio", Name: "Studio", Chat: "https://studio.example/v1", Key: "sk-studio", Models: []string{"chat-1", "clip-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelKind("studio/clip-1", provider.KindVideo); err != nil {
		t.Fatal(err)
	}
}

// Settings › Models › Images' video model: a model that makes videos is saved, one
// that doesn't (a chat model, or none such) is refused, and a save from
// another row, which doesn't send it, keeps it.
func TestVideoGenCheckedAndKept(t *testing.T) {
	home(t)
	videoModel(t)
	for _, bad := range []string{"studio/chat-1", "studio/nope", "clip-1"} {
		if _, _, err := Apply([]byte(`{"videoGen":"`+bad+`"}`), false); err == nil {
			t.Errorf("%s was taken as a video model", bad)
		}
	}
	if _, _, err := Apply([]byte(`{"videoGen":"studio/clip-1"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply([]byte(`{"theme":"dark"}`), false); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().VideoGen; got != "studio/clip-1" {
		t.Fatalf("videoGen %q after another row's save", got)
	}
	if _, _, err := Apply([]byte(`{"videoGen":""}`), false); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().VideoGen; got != "" {
		t.Fatalf("Automatic left videoGen %q", got)
	}
}

func minus(a, b []string) []string {
	var out []string
	for _, k := range a {
		if !slices.Contains(b, k) {
			out = append(out, k)
		}
	}
	return out
}

// Setting one key through Body leaves every other the user set as it
// is, and a key the save keeps when it isn't sent is set even to its
// zero value.
func TestBodySetsOneKeyOnly(t *testing.T) {
	home(t)
	s := settings.Defaults()
	s.Theme, s.NoBird, s.RedactShape, s.Currency, s.Proxy = "dark", true, true, "cny", "http://127.0.0.1:7890"
	two := 2
	s.RefusalRetries = &two
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	set := func(key, value string) settings.Settings {
		t.Helper()
		cur, err := settings.LoadChecked()
		if err != nil {
			t.Fatal(err)
		}
		body, err := Body(cur, key, json.RawMessage(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(body, false); err != nil {
			t.Fatalf("%s=%s: %v", key, value, err)
		}
		return settings.Load()
	}
	got := set("noBird", "false")
	if got.NoBird {
		t.Error("noBird set to false stayed on: a zero value was read as not sent")
	}
	got = set("lang", `"ja"`)
	if got.Lang != "ja" || got.Theme != "dark" || !got.RedactShape || got.Currency != "cny" ||
		got.Proxy != "http://127.0.0.1:7890" || got.RefusalRetries == nil || *got.RefusalRetries != 2 {
		t.Errorf("setting lang changed what the user set: %+v", got)
	}
}

// A value the save refuses writes nothing, whichever way it came in.
func TestRefusedValueWritesNothing(t *testing.T) {
	home(t)
	s := settings.Defaults()
	s.Theme = "dark"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"theme":          `"neon"`,
		"lang":           `"xx"`,
		"trayUsageEvery": `4`,
		"vision":         `"nobody/nothing"`,
		"searcher":       `"nobody/nothing"`,
		"usageAlert":     `101`,
		"updateChannel":  `"nightly"`,
		"updateSource":   `"cloudflare"`,
	} {
		cur := settings.Load()
		body, err := Body(cur, key, json.RawMessage(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(body, false); err == nil {
			t.Errorf("%s=%s was saved", key, value)
		}
		after, _ := os.ReadFile(settings.Path())
		if !bytes.Equal(before, after) {
			t.Fatalf("%s=%s was refused but settings.json changed", key, value)
		}
	}
	if _, err := Body(settings.Load(), "port", json.RawMessage(`1`)); err == nil {
		t.Error("Body took port, which has a route of its own")
	}
}

func TestNPMRegistryValidatesOnlyWhenSent(t *testing.T) {
	cur := settings.Defaults()
	// A bad hand-edited value doesn't stop unrelated Settings or CLI saves.
	cur.NPMRegistry = "ftp://npm.example/repo"
	body, err := Body(cur, "theme", json.RawMessage(`"dark"`))
	if err != nil {
		t.Fatal(err)
	}
	in, err := Merge(body, cur, false)
	if err != nil || in.NPMRegistry != cur.NPMRegistry || in.Theme != "dark" {
		t.Fatalf("unrelated save: registry %q, theme %q, err %v", in.NPMRegistry, in.Theme, err)
	}

	for _, input := range []string{
		`"ftp://npm.example/repo"`, `"https://user:private@npm.example/repo"`,
		`"https://npm.example/repo?"`, `"https://npm.example/repo#"`, `true`,
	} {
		if _, err := Merge([]byte(`{"npmRegistry":`+input+`}`), cur, false); err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("bad registry %s: %v", input, err)
		}
	}
	for _, c := range []struct{ input, want string }{
		{`"  http://127.0.0.1:8765/repository/npm///  "`, "http://127.0.0.1:8765/repository/npm"},
		{`""`, ""},
	} {
		in, err := Merge([]byte(`{"npmRegistry":`+c.input+`}`), cur, false)
		if err != nil || in.NPMRegistry != c.want {
			t.Errorf("registry %s: %q, %v; want %q", c.input, in.NPMRegistry, err, c.want)
		}
	}
}

func TestNPMRegistrySaveRefreshesManagedMCP(t *testing.T) {
	home(t)
	root, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	testenv.Program(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 1\n")
	if err := os.WriteFile(filepath.Join(bin, "codex.exe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin) // no real package manager or agent executable
	for _, key := range agentenv.Vars {
		t.Setenv(key, "")
	}
	cfg := filepath.Join(root, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[mcp_servers.private]\ncommand = 'ignore-me'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// bunx is emitted directly; this fixture never warms or runs a package.
	s := library.Server{Name: "managed", Transport: "stdio", Command: "bunx", Args: []string{"demo-mcp"}, Agents: []string{"codex"}}
	if res, err := library.SaveServer("", s); err != nil || len(res.Problems) > 0 {
		t.Fatalf("setting up managed MCP: %+v, %v", res, err)
	}
	check := func(want string) {
		t.Helper()
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Servers map[string]struct {
				Command string            `toml:"command"`
				Env     map[string]string `toml:"env"`
			} `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		managed := doc.Servers["managed"]
		if managed.Command == "" || managed.Env["BUN_CONFIG_REGISTRY"] != want || doc.Servers["private"].Command != "ignore-me" {
			t.Fatalf("registry should be %q in the managed MCP only; entries: %+v", want, doc.Servers)
		}
	}
	check("")
	if _, _, err := Apply([]byte(`{"npmRegistry":"https://first.example/npm/"}`), false); err != nil {
		t.Fatal(err)
	}
	check("https://first.example/npm/")
	if _, err := SetPluginRegistry("https://second.example/npm"); err != nil {
		t.Fatal(err)
	}
	check("https://second.example/npm/")
	if _, _, err := Apply([]byte(`{"npmRegistry":""}`), false); err != nil {
		t.Fatal(err)
	}
	check("")
}

// "Don't ask again" on the Quit question (zhujiu on Discord) holds across
// a save from a page that doesn't send quitNoAsk (the tray panel, or a
// page from before it), and Settings › General's Ask before quitting
// sending it false turns the question back on.
func TestQuitNoAskKeptUnlessSent(t *testing.T) {
	cur := settings.Defaults()
	cur.QuitNoAsk = true
	in, err := Merge([]byte(`{"theme":"dark"}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if !in.QuitNoAsk {
		t.Error("a save that didn't send quitNoAsk turned the Quit question back on")
	}
	in, err = Merge([]byte(`{"theme":"dark","quitNoAsk":false}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.QuitNoAsk {
		t.Error("quitNoAsk sent false left the Quit question off")
	}
}

// What a reply's model name says is replyModel (ZoeCats on Discord). The
// page sends it; a page from before it sends memberModel, which changes
// it only when that page's switch was moved, so its save keeps a vendor
// name it can't show; and `magpie settings reset replyModel` (Body over
// a file with memberModel written beside it) is the model asked for.
func TestReplyModelSaves(t *testing.T) {
	for _, c := range []struct {
		name string
		cur  string // replyModel saved now
		body string
		want string // replyModel saved, "" the model asked for
	}{
		{"page picks vendor", settings.ReplyMember, `{"replyModel":"vendor","memberModel":true}`, settings.ReplyVendor},
		{"page picks asked", settings.ReplyMember, `{"replyModel":""}`, ""},
		{"page picks member", "", `{"replyModel":"member"}`, settings.ReplyMember},
		{"old page, vendor kept", settings.ReplyVendor, `{"memberModel":false}`, settings.ReplyVendor},
		{"old page turns it on", settings.ReplyVendor, `{"memberModel":true}`, settings.ReplyMember},
		{"old page turns it off", settings.ReplyMember, `{"memberModel":false}`, ""},
		{"old page, member kept", settings.ReplyMember, `{"memberModel":true}`, settings.ReplyMember},
		{"another page's save", settings.ReplyVendor, `{"theme":"dark"}`, settings.ReplyVendor},
	} {
		t.Run(c.name, func(t *testing.T) {
			home(t)
			if err := settings.Save(settings.Settings{ReplyModel: c.cur}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Apply([]byte(c.body), false); err != nil {
				t.Fatal(err)
			}
			if got := settings.Load().ReplyModel; got != c.want {
				t.Errorf("replyModel %q, want %q", got, c.want)
			}
		})
	}
	t.Run("reset", func(t *testing.T) {
		home(t)
		if err := settings.Save(settings.Settings{ReplyModel: settings.ReplyMember}); err != nil {
			t.Fatal(err)
		}
		cur := settings.Load()
		if !cur.MemberModel {
			t.Fatal("memberModel isn't written beside replyModel member")
		}
		body, err := Body(cur, "replyModel", json.RawMessage(`""`))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(body, false); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load().ReplyModel; got != "" {
			t.Errorf("reset left replyModel %q", got)
		}
	})
}

// Put agents back when quitting: a save from a page that doesn't send
// restoreOnQuit (the tray panel, or a page from before it) keeps it, and
// one that sends it false turns it off.
func TestRestoreOnQuitKeptUnlessSent(t *testing.T) {
	cur := settings.Defaults()
	cur.RestoreOnQuit = true
	in, err := Merge([]byte(`{"theme":"dark"}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if !in.RestoreOnQuit {
		t.Error("a save that didn't send restoreOnQuit turned it off")
	}
	in, err = Merge([]byte(`{"theme":"dark","restoreOnQuit":false}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.RestoreOnQuit {
		t.Error("restoreOnQuit sent false left it on")
	}
}

// Show request content (llxth1 on X): a save from a page that doesn't send
// hideRequestText (the tray panel, or a page from before it) keeps it off,
// and one that sends it false turns it back on.
func TestHideRequestTextKeptUnlessSent(t *testing.T) {
	cur := settings.Defaults()
	cur.HideRequestText = true
	in, err := Merge([]byte(`{"theme":"dark"}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if !in.HideRequestText {
		t.Error("a save that didn't send hideRequestText showed request content again")
	}
	in, err = Merge([]byte(`{"theme":"dark","hideRequestText":false}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.HideRequestText {
		t.Error("hideRequestText sent false left it on")
	}
}

// netube99 on X: Settings → Images refused minimax-cn/MiniMax-H3-Image with
// "no model … to generate images" when the key serves no chat model.
func TestImageGenAcceptsMiniMaxImageModel(t *testing.T) {
	home(t)
	for _, preset := range []string{"minimax-cn", "minimax"} {
		p, err := provider.FromPreset(preset)
		if err != nil {
			t.Fatal(err)
		}
		p.ID, p.Key, p.NoModels = preset, "key", true
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"MiniMax-H3-Image", "MiniMax-H3-Image-Highspeed", "image-01"} {
			body, _ := json.Marshal(map[string]any{"imageGen": preset + "/" + m})
			if _, err := Merge(body, settings.Settings{}, true); err != nil {
				t.Errorf("%s/%s: %v", preset, m, err)
			}
		}
	}
	body, _ := json.Marshal(map[string]any{"imageGen": "minimax-cn/nothing"})
	if _, err := Merge(body, settings.Settings{}, true); err == nil {
		t.Error("a model it doesn't draw was taken")
	}
}

// The models folder is set by Move… on the redaction page, never by a
// settings save, whatever the body says.
func TestRedactModelDirIsNotASettingsSave(t *testing.T) {
	cur := settings.Defaults()
	cur.RedactModelDir = "/models"
	in, err := Merge([]byte(`{"theme":"dark","redactModelDir":"/elsewhere"}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.RedactModelDir != "/models" {
		t.Errorf("a save changed the models folder: %q", in.RedactModelDir)
	}
}
