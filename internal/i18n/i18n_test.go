package i18n

import (
	"regexp"
	"strings"
	"testing"
)

func keysOf(cat map[string]string) map[string]bool {
	out := make(map[string]bool, len(cat))
	for k := range cat {
		out[k] = true
	}
	return out
}

func TestCatalogsComplete(t *testing.T) {
	base := keysOf(catalogs[EN])
	if len(base) == 0 {
		t.Fatal("empty catalog")
	}
	for _, lang := range Langs {
		cat, ok := catalogs[lang]
		if !ok {
			t.Fatalf("missing catalog %s", lang)
		}
		got := keysOf(cat)
		for k := range base {
			if !got[k] {
				t.Errorf("%s: missing key %q", lang, k)
			}
		}
		for k := range got {
			if !base[k] {
				t.Errorf("%s: extra key %q", lang, k)
			}
		}
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0-9.]*([a-zA-Z])`)

// sample args satisfy the verbs in a template, so a translation with the wrong
// verbs or the wrong count shows up as a formatting error.
func sampleArgs(tmpl string) []any {
	var args []any
	for _, m := range verbRe.FindAllStringSubmatch(tmpl, -1) {
		switch m[1] {
		case "d":
			args = append(args, 7)
		case "f", "g", "e":
			args = append(args, 1.5)
		case "q", "t", "T", "v":
			args = append(args, "s")
		default:
			args = append(args, "s")
		}
	}
	return args
}

func TestTemplatesFormat(t *testing.T) {
	for _, lang := range Langs {
		for key, tmpl := range catalogs[lang] {
			got := T(lang, key, sampleArgs(tmpl)...)
			if strings.Contains(got, "%!") {
				t.Errorf("%s/%s: bad verbs: %q", lang, key, got)
			}
			if got == "" {
				t.Errorf("%s/%s: empty", lang, key)
			}
			if strings.HasPrefix(got, "[") && strings.HasSuffix(got, "]") && key != KeyReplyLimit {
				t.Errorf("%s/%s: missing template", lang, key)
			}
		}
	}
}

// Every catalog entry must agree on the number of verbs, otherwise one
// translation silently drops an argument.
func TestVerbCountsAgree(t *testing.T) {
	for key, tmpl := range catalogs[EN] {
		want := len(sampleArgs(tmpl))
		for _, lang := range Langs {
			if n := len(sampleArgs(catalogs[lang][key])); n != want {
				t.Errorf("%s/%s: %d verbs, en has %d", lang, key, n, want)
			}
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Lang{
		"":           "",
		"en":         EN,
		"EN-US":      EN,
		"zh":         ZH,
		"zh-CN":      ZH,
		"中文":         ZH,
		"简体中文":       ZH,
		"zh-Hant":    ZHT,
		"zh_TW":      ZHT,
		"繁體中文":       ZHT,
		"zh-hant-hk": ZHT,
		"tlh":        "",
	}
	for in, want := range cases {
		got, ok := Parse(in)
		if want == "" {
			if ok {
				t.Errorf("Parse(%q) = %q, want no match", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("Parse(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

func TestTUnknownLangFallsBack(t *testing.T) {
	if got := T(Lang("xx"), KeyStopping); got != catalogs[EN][KeyStopping] {
		t.Fatalf("got %q", got)
	}
	if got := T(ZH, "no_such_key"); got != "[no_such_key]" {
		t.Fatalf("got %q", got)
	}
}
