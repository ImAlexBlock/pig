package i18n

import (
	"fmt"
	"strings"
)

// Lang is a reply language.
type Lang string

// Supported languages.
const (
	EN  Lang = "en"
	ZH  Lang = "zh"
	ZHT Lang = "zh-Hant"
)

// Langs lists the supported languages in menu order.
var Langs = []Lang{EN, ZH, ZHT}

// Parse maps a user or config string to a language. It accepts BCP-47 tags,
// common aliases and the native names.
func Parse(s string) (Lang, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.ReplaceAll(t, "_", "-")
	switch t {
	case "en", "en-us", "en-gb", "english":
		return EN, true
	case "zh", "zh-cn", "zh-hans", "zh-sg", "chs", "cn", "chinese", "中文", "简体", "简体中文":
		return ZH, true
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo", "cht", "tw", "hk", "繁中", "繁体", "繁体中文", "繁體", "繁體中文":
		return ZHT, true
	}
	// Trailing script or region subtags: "zh-hant-hk".
	if strings.HasPrefix(t, "zh-hant") {
		return ZHT, true
	}
	if strings.HasPrefix(t, "zh-hans") || strings.HasPrefix(t, "zh-cn") {
		return ZH, true
	}
	if strings.HasPrefix(t, "en") {
		return EN, true
	}
	return "", false
}

// Canonical returns the stored form of l, falling back to EN.
func Canonical(l Lang) Lang {
	if _, ok := catalogs[l]; ok {
		return l
	}
	return EN
}

// Label is the native name of l, used in replies.
func (l Lang) Label() string {
	switch Canonical(l) {
	case ZH:
		return "中文（简体）"
	case ZHT:
		return "繁體中文"
	default:
		return "English"
	}
}

// T renders key in lang with args. An unknown key renders as [key] so a
// missing entry is visible instead of silently blank.
func T(lang Lang, key string, args ...any) string {
	cat, ok := catalogs[Canonical(lang)]
	if !ok {
		cat = catalogs[EN]
	}
	tmpl, ok := cat[key]
	if !ok {
		tmpl, ok = catalogs[EN][key]
		if !ok {
			return "[" + key + "]"
		}
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}
