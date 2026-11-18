package routing

import (
	"encoding/json"
	"strings"
)

// PopularResource is one entry in the built-in catalogue of services people
// commonly need routed through a VPN - offered as a picker so the user doesn't
// have to know that, say, YouTube also needs googlevideo.com before video
// actually plays.
//
// Domains is everything that service needs tunnelled, not just its front door.
// Getting this wrong is the classic failure of domain-based routing: the page
// loads (main domain matched) but the media/API host is a different name that
// went direct and stayed blocked, which reads to the user as "the VPN doesn't
// work" rather than "the list was incomplete".
type PopularResource struct {
	Name string `json:"name"`
	// Icon is the domain whose favicon represents the service in the UI -
	// always the first of Domains, kept explicit so the clients don't have to
	// agree on that convention separately.
	Icon    string   `json:"icon"`
	Domains []string `json:"domains"`
	// Category groups the picker into sections, in the order of
	// Categories - the clients translate the key for the header.
	Category string `json:"category"`
}

// The picker's sections, in display order.
const (
	CategorySocial = "social"
	CategoryMusic  = "music"
	CategoryAI     = "ai"
	CategoryVideo  = "video"
	CategoryOther  = "other"
)

// Categories is the section order both clients follow.
var Categories = []string{CategorySocial, CategoryMusic, CategoryAI, CategoryVideo, CategoryOther}

// PopularResources is the catalogue both clients render. Kept here rather than
// duplicated in Kotlin and JavaScript so the two can't drift apart on what
// "Instagram" means - the Windows and Android pickers are meant to be the same
// list, and a list maintained twice is a list maintained once and copied
// wrongly later.
func PopularResources() []PopularResource {
	return []PopularResource{
		// The Telegram apps don't resolve names for their own traffic - they
		// connect to Telegram's data centres by addresses built into the app,
		// so there's no DNS answer for the sniffer to learn from and the
		// domains alone only ever covered the website and t.me links. These
		// are Telegram's published ranges (core.telegram.org/resources/cidr.txt).
		resIn(CategorySocial, "Telegram", "telegram.org", "t.me", "telegram.me",
			"91.108.56.0/22", "91.108.4.0/22", "91.108.8.0/22", "91.108.16.0/22",
			"91.108.12.0/22", "149.154.160.0/20", "91.105.192.0/23", "91.108.20.0/22",
			"185.76.151.0/24", "2001:b28:f23d::/48", "2001:b28:f23f::/48",
			"2001:67c:4e8::/48", "2001:b28:f23c::/48", "2a0a:f280::/32"),
		resIn(CategorySocial, "Instagram", "instagram.com", "cdninstagram.com"),
		resIn(CategorySocial, "Facebook", "facebook.com", "fbcdn.net"),
		resIn(CategorySocial, "X", "x.com", "twitter.com", "twimg.com"),
		// Discord spreads over more than its main domains: voice servers live
		// under discord.media, attachments and avatars on discordapp.net and
		// discordcdn.com, invites and gifts on their own short domains. Voice
		// itself runs on Discord's own network (AS49544, 66.22.192.0/18), and
		// the API and gateway sit on Cloudflare addresses reserved for Discord
		// (162.159.128.0/20) - listed as ranges so an app that already holds
		// its addresses, and asks no DNS, still goes through the tunnel.
		resIn(CategorySocial, "Discord", "discord.com", "discordapp.com", "discord.gg", "discordapp.net",
			"discord.media", "discordcdn.com", "discord.co", "discord.gift", "discord.new",
			"discord.dev", "discordstatus.com", "dis.gd",
			"66.22.192.0/18", "162.159.128.0/20"),
		resIn(CategorySocial, "Signal", "signal.org", "whispersystems.org"),
		resIn(CategorySocial, "Snapchat", "snapchat.com", "sc-cdn.net"),
		resIn(CategorySocial, "LinkedIn", "linkedin.com", "licdn.com"),
		resIn(CategorySocial, "Threads", "threads.net", "threads.com"),
		resIn(CategorySocial, "TikTok", "tiktok.com", "tiktokcdn.com", "tiktokv.com"),
		resIn(CategoryMusic, "Spotify", "spotify.com", "scdn.co", "spotifycdn.com"),
		resIn(CategoryMusic, "Deezer", "deezer.com", "dzcdn.net"),
		// OpenAI and Google both refuse these to Russian IPs themselves, so
		// every host the app talks to has to come from the tunnel: the page,
		// its static assets (oaistatic) and uploaded/generated files
		// (oaiusercontent) for ChatGPT; the web app, AI Studio and the API
		// behind them for Gemini. Only Gemini's own hosts - not google.com,
		// which would drag every Google service through the tunnel.
		resIn(CategoryAI, "ChatGPT", "chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com"),
		resIn(CategoryAI, "Gemini", "gemini.google.com", "bard.google.com", "aistudio.google.com", "generativelanguage.googleapis.com"),
		// Anthropic doesn't serve Russia either: the app (claude.ai, and
		// claude.com it is moving to), the API and account pages under
		// anthropic.com, uploaded and generated files (claudeusercontent.com),
		// published artifacts (claude.site) and the clau.de short links.
		resIn(CategoryAI, "Claude", "claude.ai", "claude.com", "anthropic.com",
			"claudeusercontent.com", "claude.site", "clau.de"),
		// The Android app doesn't talk to www.youtube.com like the site does:
		// its API is youtubei/youtube.googleapis.com and avatars come from
		// ggpht.com. Missing those, the app's API went out directly while the
		// video went through the tunnel - and video URLs are bound to the IP
		// that requested them, so playback was refused. Only YouTube's own
		// googleapis hosts, not googleapis.com, which is half of Google.
		resIn(CategoryVideo, "YouTube", "youtube.com", "googlevideo.com", "ytimg.com", "youtu.be",
			"youtubei.googleapis.com", "youtube.googleapis.com", "ggpht.com"),
		resIn(CategoryVideo, "Netflix", "netflix.com", "nflxvideo.net", "nflximg.net", "nflxext.com"),
		resIn(CategoryVideo, "Twitch", "twitch.tv", "ttvnw.net", "jtvnw.net"),
		resIn(CategoryOther, "RuTracker", "rutracker.org", "rutracker.net"),
		resIn(CategoryOther, "PlayStation Store", "playstation.com", "sonyentertainmentnetwork.com", "playstation.net"),
		resIn(CategoryOther, "Notion", "notion.so", "notion.com"),
		resIn(CategoryOther, "Figma", "figma.com"),
		resIn(CategoryOther, "Canva", "canva.com"),
		resIn(CategoryOther, "Trello", "trello.com"),
		resIn(CategoryOther, "Autodesk", "autodesk.com"),
		resIn(CategoryOther, "Adobe", "adobe.com", "adobe.io", "typekit.net"),
		resIn(CategoryOther, "Wix", "wix.com", "wixsite.com"),
		resIn(CategoryOther, "Airtable", "airtable.com"),
		resIn(CategoryOther, "Zoom", "zoom.us", "zoom.com"),
		resIn(CategoryOther, "Microsoft", "microsoft.com", "live.com", "office.com"),
		resIn(CategoryOther, "Cloudflare", "cloudflare.com"),
		resIn(CategoryOther, "Booking.com", "booking.com", "bstatic.com"),
		resIn(CategoryOther, "eBay", "ebay.com", "ebayimg.com"),
	}
}

func resIn(category, name string, domains ...string) PopularResource {
	return PopularResource{Name: name, Category: category, Icon: domains[0], Domains: domains}
}

// UpgradeSites brings a saved list up to date with the catalogue: for every
// service the list already carries - its first domain is on it - the
// entries the service gained since are added. Without this, someone who had
// switched Discord on before discord.media joined it would keep the old,
// incomplete set (and see the tile as off). Returns the list unchanged, and
// false, when there was nothing to add.
func UpgradeSites(sites []string) ([]string, bool) {
	have := map[string]bool{}
	for _, s := range sites {
		have[strings.ToLower(strings.TrimSpace(s))] = true
	}
	out := sites
	changed := false
	for _, r := range PopularResources() {
		if !have[strings.ToLower(r.Domains[0])] {
			continue
		}
		for _, d := range r.Domains {
			if !have[strings.ToLower(d)] {
				out = append(out, d)
				have[strings.ToLower(d)] = true
				changed = true
			}
		}
	}
	return out, changed
}

// PopularResourcesJSON is the catalogue as a JSON array, for clients that
// cross a language boundary to read it (gomobile, Wails).
func PopularResourcesJSON() string {
	data, err := json.Marshal(PopularResources())
	if err != nil {
		return "[]"
	}
	return string(data)
}
