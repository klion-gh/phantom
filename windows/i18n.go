package main

import "sync/atomic"

// The WebView UI has its own (JS) translation table in frontend/src/i18n.js;
// this file is only the Go-side tray menu, which shares the same persisted
// language (loadLanguage/saveLanguage) so the two stay in sync.

var trayStrings = map[string]map[string]string{
	"ru": {
		"connect":         "Подключить",
		"disconnect":      "Отключить",
		"open":            "Открыть",
		"quit":            "Выход",
		"toggle_tip":      "Подключить/отключить последний активный профиль",
		"open_tip":        "Открыть окно Phantom",
		"quit_tip":        "Полностью закрыть Phantom",
		"vpn_on":          "подключён",
		"vpn_off":         "отключён",
		"proxy_on":        "активен",
		"proxy_off":       "отключён",
		"pick_app_title":  "Выбрать приложение",
		"programs_filter": "Программы (*.exe)",
	},
	"en": {
		"connect":         "Connect",
		"disconnect":      "Disconnect",
		"open":            "Open",
		"quit":            "Quit",
		"toggle_tip":      "Connect/disconnect the last active profile",
		"open_tip":        "Open the Phantom window",
		"quit_tip":        "Quit Phantom completely",
		"vpn_on":          "on",
		"vpn_off":         "off",
		"proxy_on":        "active",
		"proxy_off":       "off",
		"pick_app_title":  "Pick an application",
		"programs_filter": "Programs (*.exe)",
	},
}

// trayT looks up key for lang, falling back to Russian then to the key itself.
func trayT(lang, key string) string {
	if table, ok := trayStrings[lang]; ok {
		if s, ok := table[key]; ok {
			return s
		}
	}
	if s, ok := trayStrings["ru"][key]; ok {
		return s
	}
	return key
}

// trayLang is read by pollTrayStatus (its own goroutine) and written by
// App.SetLanguage (the Wails call thread), so it goes through an atomic.
var trayLang atomic.Value // string

func getTrayLang() string {
	if v, ok := trayLang.Load().(string); ok {
		return v
	}
	return "ru"
}

func setTrayLang(lang string) {
	if lang != "en" {
		lang = "ru"
	}
	trayLang.Store(lang)
}
