package theming

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinTheme(t *testing.T) {
	tests := []struct {
		name     string
		theme    string
		wantBlue string
		wantBase string
	}{
		{"default by empty name", "", "#89b4fa", "#1e1e2e"},
		{"known builtin", "nord", "#81a1c1", "#2e3440"},
		{"unknown falls back to default", "no-such-theme", "#89b4fa", "#1e1e2e"},
		{"transparent keeps literal base", "transparent", "#89b4fa", "transparent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuiltinTheme(tt.theme)
			if got.Palette.Blue != tt.wantBlue {
				t.Errorf("Blue = %q, want %q", got.Palette.Blue, tt.wantBlue)
			}
			if got.Palette.Base != tt.wantBase {
				t.Errorf("Base = %q, want %q", got.Palette.Base, tt.wantBase)
			}
		})
	}
}

// TestBuiltinThemesComplete guards the ported table: every theme must fill
// all nine tokens (a missed field would silently emit `""` into the palette
// file).
func TestBuiltinThemesComplete(t *testing.T) {
	for name, theme := range builtinThemes {
		for _, token := range paletteFileTokens {
			if theme.Palette.token(token) == "" {
				t.Errorf("builtin %q: token %q is empty", name, token)
			}
		}
	}
	if len(builtinThemes) != 19 {
		t.Errorf("builtin theme count = %d, want 19 (themes.ts BUILTIN_THEMES)", len(builtinThemes))
	}
}

func TestParseExternalTheme(t *testing.T) {
	tests := []struct {
		name string
		json string
		// want nil?
		wantNil bool
		// checks on the accepted result
		wantName     string
		wantNameSet  bool
		wantVariant  string
		wantResolved map[string]string // token -> resolved palette value
	}{
		{name: "invalid json", json: `{nope`, wantNil: true},
		{name: "json null", json: `null`, wantNil: true},
		{name: "array document", json: `[1,2]`, wantNil: true},
		{name: "empty object rejected", json: `{}`, wantNil: true},
		{name: "empty-string name alone rejected", json: `{"name":""}`, wantNil: true},
		{name: "invalid variant alone rejected", json: `{"variant":"purple"}`, wantNil: true},
		{name: "empty palette value rejected", json: `{"palette":{"blue":""}}`, wantNil: true},
		{name: "non-string palette value rejected", json: `{"palette":{"blue":123}}`, wantNil: true},
		{name: "unknown palette token alone rejected", json: `{"palette":{"bogus":"#fff"}}`, wantNil: true},
		{name: "status null rejected", json: `{"status":null}`, wantNil: true},
		{
			name: "name only accepted",
			json: `{"name":"dayfox"}`, wantName: "dayfox", wantNameSet: true,
			wantResolved: map[string]string{"blue": "#89b4fa"}, // default palette
		},
		{
			name: "variant only accepted",
			json: `{"variant":"light"}`, wantVariant: "light",
		},
		{
			// TS accepts any of the 21 known tokens even when this package
			// models none of them — resolution is then the default palette.
			name: "unmodeled known token accepted",
			json: `{"palette":{"lavender":"#b4befe"}}`,
			wantResolved: map[string]string{
				"blue": "#89b4fa", "base": "#1e1e2e",
			},
		},
		{
			name: "status object alone accepted (forward-compat pass-through)",
			json: `{"status":{}}`,
		},
		{
			name: "icons object alone accepted (forward-compat pass-through)",
			json: `{"icons":{"idle":"o"}}`,
		},
		{
			name:     "palette merges over default",
			json:     `{"name":"custom","palette":{"blue":"#123456","base":"#000000"}}`,
			wantName: "custom", wantNameSet: true,
			wantResolved: map[string]string{
				"blue": "#123456", "base": "#000000",
				"green": "#a6e3a1", // untouched token falls back to default
			},
		},
		{
			name:     "empty-string name with palette keeps empty name",
			json:     `{"name":"","palette":{"blue":"#123456"}}`,
			wantName: "", wantNameSet: true,
			wantResolved: map[string]string{"blue": "#123456"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseExternalTheme([]byte(tt.json))
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ParseExternalTheme = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ParseExternalTheme = nil, want accepted theme")
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.nameSet != tt.wantNameSet {
				t.Errorf("nameSet = %v, want %v", got.nameSet, tt.wantNameSet)
			}
			if got.Variant != tt.wantVariant {
				t.Errorf("Variant = %q, want %q", got.Variant, tt.wantVariant)
			}
			resolved := got.Resolve()
			for token, want := range tt.wantResolved {
				if v := resolved.Palette.token(token); v != want {
					t.Errorf("resolved %s = %q, want %q", token, v, want)
				}
			}
		})
	}
}

func TestExternalThemeResolvedName(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"explicit name wins", `{"name":"dayfox"}`, "dayfox"},
		// TS effectiveThemeName uses ?? — an explicit "" is NOT nullish.
		{"explicit empty name stays empty", `{"name":"","palette":{"blue":"#123456"}}`, ""},
		{"absent name falls back", `{"palette":{"blue":"#123456"}}`, "config-theme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := ParseExternalTheme([]byte(tt.json))
			if ext == nil {
				t.Fatal("ParseExternalTheme = nil")
			}
			if got := ext.ResolvedName("config-theme"); got != tt.want {
				t.Errorf("ResolvedName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveActiveTheme(t *testing.T) {
	tests := []struct {
		name          string
		configJSON    string // "" = no file
		activeJSON    string // "" = no file
		wantBlue      string
		wantThemeName string
	}{
		{
			name:     "no files: default theme, empty name",
			wantBlue: "#89b4fa", wantThemeName: "",
		},
		{
			name:       "config string theme",
			configJSON: `{"theme":"nord"}`,
			wantBlue:   "#81a1c1", wantThemeName: "nord",
		},
		{
			// index.ts: `typeof config.theme === "string"` — inline theme
			// objects in config.json never feed the palette/header path.
			name:       "config inline object theme ignored",
			configJSON: `{"theme":{"palette":{"blue":"#111111"}}}`,
			wantBlue:   "#89b4fa", wantThemeName: "",
		},
		{
			name:       "external wins over config",
			configJSON: `{"theme":"nord"}`,
			activeJSON: `{"name":"dayfox","palette":{"blue":"#2848a9"}}`,
			wantBlue:   "#2848a9", wantThemeName: "dayfox",
		},
		{
			name:       "rejected external falls back to config",
			configJSON: `{"theme":"nord"}`,
			activeJSON: `{"palette":{"blue":""}}`,
			wantBlue:   "#81a1c1", wantThemeName: "nord",
		},
		{
			name:       "external without name uses config name",
			configJSON: `{"theme":"nord"}`,
			activeJSON: `{"palette":{"blue":"#2848a9"}}`,
			wantBlue:   "#2848a9", wantThemeName: "nord",
		},
		{
			// External merges over the DEFAULT theme, not the config
			// builtin — resolveTheme(PartialTheme) semantics.
			name:       "external merge base is default, not config builtin",
			configJSON: `{"theme":"nord"}`,
			activeJSON: `{"name":"x","palette":{"red":"#aa0000"}}`,
			wantBlue:   "#89b4fa", wantThemeName: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.configJSON != "" {
				mustWrite(t, filepath.Join(dir, "config.json"), tt.configJSON)
			}
			if tt.activeJSON != "" {
				mustWrite(t, filepath.Join(dir, "active-theme.json"), tt.activeJSON)
			}
			theme, name := ResolveActiveTheme(dir)
			if theme.Palette.Blue != tt.wantBlue {
				t.Errorf("Blue = %q, want %q", theme.Palette.Blue, tt.wantBlue)
			}
			if name != tt.wantThemeName {
				t.Errorf("themeName = %q, want %q", name, tt.wantThemeName)
			}
		})
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
