package steam

import (
	"net/http"
	"sync"
	"time"
)

// Game is region-normalised game metadata from the store appdetails endpoint.
type Game struct {
	AppID            int         `json:"appid"`
	Name             string      `json:"name"`
	Type             string      `json:"type"` // "game", "dlc", "demo", ...
	IsFree           bool        `json:"is_free"`
	HeaderImage      string      `json:"header_image"`
	CapsuleImage     string      `json:"capsule_image"`
	ShortDescription string      `json:"short_description"`
	Developers       []string    `json:"developers"`
	Publishers       []string    `json:"publishers"`
	Genres           []string    `json:"genres"`
	Categories       []string    `json:"categories"` // e.g. "Multi-player", "Co-op", "Online PvP"
	ReleaseDate      string      `json:"release_date"`
	ComingSoon       bool        `json:"coming_soon"`
	Platforms        []string    `json:"platforms"` // subset of "windows","mac","linux"
	Price            *Price      `json:"price,omitempty"`
	Metacritic       *Metacritic `json:"metacritic,omitempty"`
	// Recommendations is the total number of user reviews Steam reports for the
	// app (appdetails "recommendations.total"). 0 when absent.
	Recommendations int `json:"recommendations"`
}

// Price is region-normalised pricing info from the store appdetails endpoint.
type Price struct {
	Currency        string `json:"currency"` // e.g. "USD", "PLN"
	Initial         int64  `json:"initial"`  // price in minor units (cents), before discount
	Final           int64  `json:"final"`    // price in minor units, after discount
	DiscountPercent int    `json:"discount_percent"`
	FormattedFinal  string `json:"formatted_final"` // Steam's own formatted string
}

// Metacritic holds the Metacritic score and review URL for a game, when Steam
// reports one.
type Metacritic struct {
	Score int    `json:"score"`
	URL   string `json:"url"`
}

// SearchResult is one entry from the store search endpoint.
type SearchResult struct {
	AppID        int    `json:"appid"`
	Name         string `json:"name"`
	TinyImage    string `json:"tiny_image"`
	Metascore    string `json:"metascore"` // Steam sends "" when absent
	Currency     string `json:"currency"`
	PriceFinal   int64  `json:"price_final"` // minor units; 0 when free/unknown
	PriceInitial int64  `json:"price_initial"`
}

// Reviews is the aggregate review summary for an app.
type Reviews struct {
	TotalPositive int `json:"total_positive"`
	TotalNegative int `json:"total_negative"`
	TotalReviews  int `json:"total_reviews"`
	// Description is Steam's label, e.g. "Overwhelmingly Positive".
	Description string `json:"description"`
}

// Player is a public Steam profile summary.
type Player struct {
	SteamID     string `json:"steamid"`
	PersonaName string `json:"personaname"`
	Avatar      string `json:"avatar"` // 64px url
	ProfileURL  string `json:"profile_url"`
}

// Client is a small client for the public Steam store/community HTTP APIs.
// The zero value is not ready to use; construct one with New.
type Client struct {
	HTTP      *http.Client
	StoreBase string // default "https://store.steampowered.com"
	APIBase   string // default "https://api.steampowered.com"
	APIKey    string // optional; required by PlayerSummary and OwnedGames
	UserAgent string

	// MinInterval is the minimum spacing enforced between consecutive store
	// requests made by this client. Defaults to ~120ms; tests may shrink it.
	MinInterval time.Duration

	mu       sync.Mutex
	lastCall time.Time
}
