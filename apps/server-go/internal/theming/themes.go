package theming

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Palette is the palette-token subset the palette file and header sync
// actually read. themes.ts models 21 tokens per theme; the two writers
// consume exactly these nine (the palette file emits all nine; the header's
// severity colours use blue/yellow/green/surface2/red). The other twelve
// tokens are provably dead for this package and are not ported — see
// externalPaletteTokens for how external themes referencing them are still
// accepted.
type Palette struct {
	Base     string
	Text     string
	Blue     string
	Surface0 string
	Surface2 string
	Overlay0 string
	Yellow   string
	Red      string
	Green    string
}

// Theme is the resolved theme these writers consume. themes.ts also carries
// status/icons maps; those are TUI-panel concerns and never read here.
type Theme struct {
	Palette Palette
}

// token returns the palette value for a tmux-palette-file token name.
func (p Palette) token(name string) string {
	switch name {
	case "base":
		return p.Base
	case "text":
		return p.Text
	case "blue":
		return p.Blue
	case "surface0":
		return p.Surface0
	case "surface2":
		return p.Surface2
	case "overlay0":
		return p.Overlay0
	case "yellow":
		return p.Yellow
	case "red":
		return p.Red
	case "green":
		return p.Green
	}
	return ""
}

// setToken overwrites a modeled palette token; reports whether the name is
// modeled (external themes may carry the other twelve themes.ts tokens,
// which merge to nothing here because no writer reads them).
func (p *Palette) setToken(name, value string) bool {
	switch name {
	case "base":
		p.Base = value
	case "text":
		p.Text = value
	case "blue":
		p.Blue = value
	case "surface0":
		p.Surface0 = value
	case "surface2":
		p.Surface2 = value
	case "overlay0":
		p.Overlay0 = value
	case "yellow":
		p.Yellow = value
	case "red":
		p.Red = value
	case "green":
		p.Green = value
	default:
		return false
	}
	return true
}

// DefaultThemeName mirrors DEFAULT_THEME in themes.ts.
const DefaultThemeName = "catppuccin-mocha"

// builtinThemes is BUILTIN_THEMES from themes.ts, reduced to the nine
// palette tokens this package reads. Values are verbatim.
var builtinThemes = map[string]Theme{
	"catppuccin-mocha": {Palette: Palette{
		Base: "#1e1e2e", Text: "#cdd6f4", Blue: "#89b4fa", Surface0: "#313244", Surface2: "#585b70",
		Overlay0: "#6c7086", Yellow: "#f9e2af", Red: "#f38ba8", Green: "#a6e3a1",
	}},
	"catppuccin-latte": {Palette: Palette{
		Base: "#eff1f5", Text: "#4c4f69", Blue: "#1e66f5", Surface0: "#ccd0da", Surface2: "#acb0be",
		Overlay0: "#9ca0b0", Yellow: "#df8e1d", Red: "#d20f39", Green: "#40a02b",
	}},
	"catppuccin-frappe": {Palette: Palette{
		Base: "#303446", Text: "#c6d0f5", Blue: "#8da4e2", Surface0: "#414559", Surface2: "#626880",
		Overlay0: "#626880", Yellow: "#e5c890", Red: "#e78284", Green: "#a6d189",
	}},
	"catppuccin-macchiato": {Palette: Palette{
		Base: "#24273a", Text: "#cad3f5", Blue: "#8aadf4", Surface0: "#363a4f", Surface2: "#5b6078",
		Overlay0: "#5b6078", Yellow: "#eed49f", Red: "#ed8796", Green: "#a6da95",
	}},
	"tokyo-night": {Palette: Palette{
		Base: "#1a1b26", Text: "#c0caf5", Blue: "#7aa2f7", Surface0: "#24283b", Surface2: "#343a52",
		Overlay0: "#565f89", Yellow: "#e0af68", Red: "#f7768e", Green: "#9ece6a",
	}},
	"gruvbox-dark": {Palette: Palette{
		Base: "#282828", Text: "#ebdbb2", Blue: "#83a598", Surface0: "#3c3836", Surface2: "#665c54",
		Overlay0: "#665c54", Yellow: "#fabd2f", Red: "#fb4934", Green: "#b8bb26",
	}},
	"nord": {Palette: Palette{
		Base: "#2e3440", Text: "#eceff4", Blue: "#81a1c1", Surface0: "#3b4252", Surface2: "#4c566a",
		Overlay0: "#4c566a", Yellow: "#ebcb8b", Red: "#bf616a", Green: "#a3be8c",
	}},
	"dracula": {Palette: Palette{
		Base: "#282a36", Text: "#f8f8f2", Blue: "#8be9fd", Surface0: "#44475a", Surface2: "#6272a4",
		Overlay0: "#6272a4", Yellow: "#f1fa8c", Red: "#ff5555", Green: "#50fa7b",
	}},
	"github-dark": {Palette: Palette{
		Base: "#0d1117", Text: "#c9d1d9", Blue: "#58a6ff", Surface0: "#161b22", Surface2: "#30363d",
		Overlay0: "#484f58", Yellow: "#e3b341", Red: "#f85149", Green: "#3fb950",
	}},
	"one-dark": {Palette: Palette{
		Base: "#282c34", Text: "#abb2bf", Blue: "#61afef", Surface0: "#3e4451", Surface2: "#5c6370",
		Overlay0: "#5c6370", Yellow: "#e5c07b", Red: "#e06c75", Green: "#98c379",
	}},
	"kanagawa": {Palette: Palette{
		Base: "#1F1F28", Text: "#DCD7BA", Blue: "#7E9CD8", Surface0: "#363646", Surface2: "#727169",
		Overlay0: "#727169", Yellow: "#D7A657", Red: "#E82424", Green: "#98BB6C",
	}},
	"everforest": {Palette: Palette{
		Base: "#2d353b", Text: "#d3c6aa", Blue: "#7fbbb3", Surface0: "#343f44", Surface2: "#475258",
		Overlay0: "#7a8478", Yellow: "#dbbc7f", Red: "#e67e80", Green: "#a7c080",
	}},
	"material": {Palette: Palette{
		Base: "#263238", Text: "#eeffff", Blue: "#82aaff", Surface0: "#37474f", Surface2: "#546e7a",
		Overlay0: "#546e7a", Yellow: "#ffcb6b", Red: "#f07178", Green: "#c3e88d",
	}},
	"cobalt2": {Palette: Palette{
		Base: "#193549", Text: "#ffffff", Blue: "#0088ff", Surface0: "#1f4662", Surface2: "#2d5a7b",
		Overlay0: "#2d5a7b", Yellow: "#ffc600", Red: "#ff0088", Green: "#9eff80",
	}},
	"flexoki": {Palette: Palette{
		Base: "#100F0F", Text: "#CECDC3", Blue: "#4385BE", Surface0: "#282726", Surface2: "#403E3C",
		Overlay0: "#6F6E69", Yellow: "#D0A215", Red: "#D14D41", Green: "#879A39",
	}},
	"ayu": {Palette: Palette{
		Base: "#0B0E14", Text: "#BFBDB6", Blue: "#59C2FF", Surface0: "#0D1017", Surface2: "#11151C",
		Overlay0: "#565B66", Yellow: "#E6B450", Red: "#D95757", Green: "#7FD962",
	}},
	"aura": {Palette: Palette{
		Base: "#15141b", Text: "#edecee", Blue: "#82e2ff", Surface0: "#1a1a24", Surface2: "#2d2d2d",
		Overlay0: "#6d6d6d", Yellow: "#ffca85", Red: "#ff6767", Green: "#9dff65",
	}},
	"matrix": {Palette: Palette{
		Base: "#0a0e0a", Text: "#62ff94", Blue: "#30b3ff", Surface0: "#141c12", Surface2: "#1e2a1b",
		Overlay0: "#2e4a37", Yellow: "#e6ff57", Red: "#ff4b4b", Green: "#62ff94",
	}},
	// "transparent" stores the literal string "transparent" for base
	// surfaces; toTmuxColour translates it to tmux's "default".
	"transparent": {Palette: Palette{
		Base: "transparent", Text: "#cdd6f4", Blue: "#89b4fa", Surface0: "#313244", Surface2: "#585b70",
		Overlay0: "#6c7086", Yellow: "#f9e2af", Red: "#f38ba8", Green: "#a6e3a1",
	}},
}

// BuiltinTheme ports resolveTheme(string | undefined) from themes.ts: a
// known builtin name resolves to its theme; unknown names and "" fall back
// to the default.
func BuiltinTheme(name string) Theme {
	if t, ok := builtinThemes[name]; ok {
		return t
	}
	return builtinThemes[DefaultThemeName]
}

// externalPaletteTokens is the full knownTokens list from themes.ts
// loadExternalTheme. All 21 are checked so the accept/reject decision
// matches TS exactly (a file supplying only e.g. "lavender" is still a
// valid external theme that suppresses the config theme), even though only
// the nine modeled tokens affect output.
var externalPaletteTokens = []string{
	"blue", "lavender", "pink", "mauve",
	"yellow", "green", "red", "peach",
	"teal", "sky",
	"text", "subtext0", "subtext1",
	"overlay0", "overlay1",
	"surface0", "surface1", "surface2",
	"base", "mantle", "crust",
}

// ExternalTheme is the accepted result of parsing active-theme.json —
// themes.ts's PartialTheme, reduced to what the writers consume. status and
// icons payloads are parsed for the acceptance check only (they are
// forward-compat pass-through in TS, unused by panel and header alike).
type ExternalTheme struct {
	// Name is the human-readable label (e.g. written by the-themer).
	Name string
	// Variant is "light" or "dark" when supplied; informational only.
	Variant string

	// nameSet distinguishes an explicit (possibly empty) name from an
	// absent one: TS's `externalTheme?.name ?? currentTheme` falls back
	// only when name is missing, not when it is "".
	nameSet bool
	palette map[string]string // modeled tokens present in the file
}

// ParseExternalTheme ports loadExternalTheme from themes.ts. Returns nil on
// any failure — invalid JSON, non-object document, or no recognisable
// fields — so the caller falls back to the configured builtin theme.
func ParseExternalTheme(data []byte) *ExternalTheme {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return nil
	}

	ext := &ExternalTheme{}

	var name string
	if v, ok := raw["name"]; ok && json.Unmarshal(v, &name) == nil {
		ext.Name = name
		ext.nameSet = true
	}
	var variant string
	if v, ok := raw["variant"]; ok && json.Unmarshal(v, &variant) == nil {
		if variant == "light" || variant == "dark" {
			ext.Variant = variant
		}
	}

	paletteRecognised := false
	if v, ok := raw["palette"]; ok {
		var src map[string]json.RawMessage
		if json.Unmarshal(v, &src) == nil && src != nil {
			for _, token := range externalPaletteTokens {
				tv, present := src[token]
				if !present {
					continue
				}
				var s string
				if json.Unmarshal(tv, &s) != nil || s == "" {
					continue
				}
				paletteRecognised = true
				// Only modeled tokens are kept; the rest count toward
				// acceptance but merge to nothing (no writer reads them).
				var probe Palette
				if probe.setToken(token, s) {
					if ext.palette == nil {
						ext.palette = map[string]string{}
					}
					ext.palette[token] = s
				}
			}
		}
	}

	// status/icons: presence of a JSON object is enough to accept the file
	// (TS passes them through verbatim; nothing here reads them).
	statusPresent := isJSONObjectField(raw, "status")
	iconsPresent := isJSONObjectField(raw, "icons")

	// Reject if nothing recognisable was found — mirrors the TS falsiness
	// check, where an empty-string name does not count.
	if ext.Name == "" && ext.Variant == "" && !paletteRecognised && !statusPresent && !iconsPresent {
		return nil
	}
	return ext
}

func isJSONObjectField(raw map[string]json.RawMessage, key string) bool {
	v, ok := raw[key]
	if !ok {
		return false
	}
	var m map[string]json.RawMessage
	return json.Unmarshal(v, &m) == nil && m != nil
}

// Resolve merges the external palette over the default builtin theme,
// mirroring resolveTheme(PartialTheme) in themes.ts (merge base is always
// the default theme, never the configured builtin).
func (e *ExternalTheme) Resolve() Theme {
	theme := BuiltinTheme(DefaultThemeName)
	for token, value := range e.palette {
		theme.Palette.setToken(token, value)
	}
	return theme
}

// ResolvedName ports index.ts effectiveThemeName's nullish coalescing:
// the fallback applies only when the external file carried no "name" key
// at all; an explicit empty name stays empty.
func (e *ExternalTheme) ResolvedName(fallback string) string {
	if e.nameSet {
		return e.Name
	}
	return fallback
}

// ResolveActiveTheme resolves the theme the writers should apply, with the
// index.ts precedence: external active-theme.json (when it parses to a
// non-nil ExternalTheme) wins over config.json's theme field, which falls
// back to the builtin default. It re-reads disk on every call — the Go
// server has no resident theme state (matches state.Builder.themeConfig's
// per-build reads).
//
// Note: like index.ts (`typeof config.theme === "string"`), only a string
// theme name in config.json feeds this path; an inline theme object is
// ignored by the palette/header writers.
func ResolveActiveTheme(configDir string) (Theme, string) {
	configName := configThemeName(configDir)
	if data, err := os.ReadFile(filepath.Join(configDir, "active-theme.json")); err == nil {
		if ext := ParseExternalTheme(data); ext != nil {
			return ext.Resolve(), ext.ResolvedName(configName)
		}
	}
	return BuiltinTheme(configName), configName
}

// configThemeName reads config.json's theme field when it is a JSON string.
func configThemeName(configDir string) string {
	raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Theme json.RawMessage `json:"theme"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	var name string
	if json.Unmarshal(cfg.Theme, &name) == nil {
		return name
	}
	return ""
}
