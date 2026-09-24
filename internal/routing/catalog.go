package routing

import "encoding/json"

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
}

// PopularResources is the catalogue both clients render. Kept here rather than
// duplicated in Kotlin and JavaScript so the two can't drift apart on what
// "Instagram" means - the Windows and Android pickers are meant to be the same
// list, and a list maintained twice is a list maintained once and copied
// wrongly later.
func PopularResources() []PopularResource {
	return []PopularResource{
		res("Telegram", "telegram.org", "t.me", "telegram.me"),
		res("RuTracker", "rutracker.org", "rutracker.net"),
		res("Instagram", "instagram.com", "cdninstagram.com"),
		res("Facebook", "facebook.com", "fbcdn.net"),
		res("X", "x.com", "twitter.com", "twimg.com"),
		res("Discord", "discord.com", "discordapp.com", "discord.gg", "discordapp.net"),
		res("Signal", "signal.org", "whispersystems.org"),
		res("Snapchat", "snapchat.com", "sc-cdn.net"),
		res("LinkedIn", "linkedin.com", "licdn.com"),
		res("Threads", "threads.net", "threads.com"),
		// OpenAI and Google both refuse these to Russian IPs themselves, so
		// every host the app talks to has to come from the tunnel: the page,
		// its static assets (oaistatic) and uploaded/generated files
		// (oaiusercontent) for ChatGPT; the web app, AI Studio and the API
		// behind them for Gemini. Only Gemini's own hosts - not google.com,
		// which would drag every Google service through the tunnel.
		res("ChatGPT", "chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com"),
		res("Gemini", "gemini.google.com", "bard.google.com", "aistudio.google.com", "generativelanguage.googleapis.com"),
		res("YouTube", "youtube.com", "googlevideo.com", "ytimg.com", "youtu.be"),
		res("Netflix", "netflix.com", "nflxvideo.net", "nflximg.net", "nflxext.com"),
		res("Spotify", "spotify.com", "scdn.co", "spotifycdn.com"),
		res("Twitch", "twitch.tv", "ttvnw.net", "jtvnw.net"),
		res("TikTok", "tiktok.com", "tiktokcdn.com", "tiktokv.com"),
		res("Deezer", "deezer.com", "dzcdn.net"),
		res("PlayStation Store", "playstation.com", "sonyentertainmentnetwork.com", "playstation.net"),
		res("Notion", "notion.so", "notion.com"),
		res("Figma", "figma.com"),
		res("Canva", "canva.com"),
		res("Trello", "trello.com"),
		res("Slack", "slack.com", "slack-edge.com"),
		res("Autodesk", "autodesk.com"),
		res("Adobe", "adobe.com", "adobe.io", "typekit.net"),
		res("Wix", "wix.com", "wixsite.com"),
		res("Airtable", "airtable.com"),
		res("Zoom", "zoom.us", "zoom.com"),
		res("Microsoft", "microsoft.com", "live.com", "office.com"),
		res("Cloudflare", "cloudflare.com"),
		res("Booking.com", "booking.com", "bstatic.com"),
		res("eBay", "ebay.com", "ebayimg.com"),
	}
}

func res(name string, domains ...string) PopularResource {
	return PopularResource{Name: name, Icon: domains[0], Domains: domains}
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
