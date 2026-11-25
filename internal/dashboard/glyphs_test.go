package dashboard

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// glyphFile maps each mark of a Go text to the icon that the dashboard draws for it.
const glyphFile = "../../web/src/lib/glyphs.json"

// glyphDirs are the packages whose string literals can reach the dashboard.
var glyphDirs = []string{"gateway", "goals", "agent", "cron", "app", "dashboard"}

// glyphKept holds the characters that the dashboard shows as text, and the parts of an emoji
// sequence.
var glyphKept = map[rune]bool{
	'·': true, '—': true, '…': true, '→': true, '←': true, '«': true, '»': true,
	'↵': true, '↑': true, '↓': true, '©': true, '®': true, '™': true,
	'\uFE0F': true, '\u200D': true, '\u20E3': true,
}

// glyphSkip holds the runes that are text, not marks. Each entry gives the reason.
var glyphSkip = map[rune]string{
	'–': "an en dash in a number range, for example 1–20; it is punctuation",
	'›': "a separator between two names in a label, for example Telegram › chat; it is punctuation",
	'沉': "a Chinese letter of the silent marker of a cron answer",
	'静': "a Chinese letter of the silent marker of a cron answer",
	'默': "a Chinese letter of the silent marker of a cron answer",
}

type glyphRow struct {
	Key  string `json:"key"`
	Icon string `json:"icon"`
	Tone string `json:"tone"`
	Word string `json:"word"`
}

type glyphData struct {
	Glyphs map[string]glyphRow `json:"glyphs"`
	Tools  map[string]string   `json:"tools"`
}

func readGlyphs(t *testing.T) glyphData {
	t.Helper()
	b, err := os.ReadFile(glyphFile)
	if err != nil {
		t.Fatalf("cannot read web/src/lib/glyphs.json: %v", err)
	}
	var g glyphData
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("cannot parse web/src/lib/glyphs.json: %v", err)
	}
	return g
}

func parseGo(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("cannot parse %s: %v", path, err)
	}
	return fset, f
}

// TestGlyphsCover makes sure that the dashboard has an icon for each mark of a Go text and for
// each tool of the status line.
func TestGlyphsCover(t *testing.T) {
	g := readGlyphs(t)

	t.Run("marks", func(t *testing.T) {
		missing := map[rune]string{}
		for _, dir := range glyphDirs {
			files, err := filepath.Glob(filepath.Join("..", dir, "*.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range files {
				if strings.HasSuffix(path, "_test.go") {
					continue
				}
				fset, f := parseGo(t, path)
				ast.Inspect(f, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						return true
					}
					for _, r := range s {
						if r < 0x2000 || glyphKept[r] || (r >= 0x2500 && r <= 0x259F) {
							continue
						}
						if _, ok := glyphSkip[r]; ok {
							continue
						}
						if _, ok := g.Glyphs[string(r)]; ok {
							continue
						}
						if _, ok := missing[r]; !ok {
							missing[r] = fset.Position(lit.Pos()).String()
						}
					}
					return true
				})
			}
		}
		runes := make([]rune, 0, len(missing))
		for r := range missing {
			runes = append(runes, r)
		}
		slices.Sort(runes)
		for _, r := range runes {
			t.Errorf("%c (U+%04X) at %s has no row in glyphs of web/src/lib/glyphs.json. "+
				"Add a row, or add the rune to glyphSkip with the reason.", r, r, missing[r])
		}
	})

	t.Run("tools", func(t *testing.T) {
		_, f := parseGo(t, "../tools/emoji.go")
		var names []string
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "toolEmoji" || len(vs.Values) != 1 {
				return true
			}
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				return false
			}
			for _, e := range lit.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.BasicLit)
				if !ok {
					continue
				}
				if name, err := strconv.Unquote(key.Value); err == nil {
					names = append(names, name)
				}
			}
			return false
		})
		if len(names) == 0 {
			t.Fatal("found no toolEmoji map literal in internal/tools/emoji.go")
		}
		for _, name := range names {
			if _, ok := g.Tools[name]; !ok {
				t.Errorf("tool %q has no row in tools of web/src/lib/glyphs.json. Give it a Lucide icon.", name)
			}
		}
	})
}
