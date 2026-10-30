package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"playpile/internal/money"
	"playpile/internal/pile"
	"playpile/internal/store"
)

// baseView is the chrome every page shares.
type baseView struct {
	User     store.User
	LoggedIn bool
	CSRF     string
	Flash    flash
	Version  string
	DevLogin bool
	Year     int
}

// base collects the shared page data.
func (s *Server) base(r *http.Request) baseView {
	b := baseView{
		Flash:    flashOf(r),
		Version:  s.cfg.Version,
		DevLogin: s.cfg.DevLogin,
		Year:     time.Now().Year(),
	}
	if sess, user, ok := s.session(r); ok {
		b.User = user
		b.LoggedIn = true
		b.CSRF = sess.CSRF
	}
	return b
}

// --- home -------------------------------------------------------------------

type homeView struct {
	baseView
	Crews           []store.CrewCard
	SteamAPIKey     bool
	DefaultCountry  string
	DefaultLanguage string
}

// --- crew list --------------------------------------------------------------

// summaryVM is the at-a-glance strip.
type summaryVM struct {
	Currency     string
	Total        string
	MyCost       string
	GroupCost    string
	Ready        int
	ReadyOf      int
	Priced       int
	Free         int
	Unpriced     int
	OtherMoney   int
	MissingForMe int
	Vetoed       int
	Members      int
	Note         string
}

// ownerVM is one ownership bubble.
type ownerVM struct {
	Initial string
	Name    string
	IsMe    bool
	// Avatar is the member's Steam avatar URL, empty when Steam knows none.
	// The bubble falls back to Initial, so this may stay empty.
	Avatar string
}

// entryVM is one game row, fully pre-rendered.
type entryVM struct {
	IsManual      bool
	CanEditManual bool
	EditURL       string
	StoreURL      string
	HasMetaScore  bool
	ID            int64
	AppID         int
	CrewID        int64
	CSRF          string
	CanRemove     bool
	Name          string
	URL           string
	Cover         string
	Sub           string
	Note          string
	AddedBy       string
	Score         int
	ScoreSign     string
	MyVote        int
	Up            int
	Down          int
	Vetoed        bool // disqualified by at least one hard no
	// VetoedStart marks the first vetoed row, so the page can draw one
	// "disqualified" divider above the block instead of one per row.
	VetoedStart bool

	MetaScore int
	MetaClass string
	MetaURL   string

	ReviewDesc  string
	ReviewPct   int
	ReviewClass string
	ReviewCount int

	PriceLabel  string
	Discount    int
	CostForMe   string
	IsFree      bool
	ComingSoon  bool
	Played      bool
	Available   bool
	OwnedByMe   bool
	OwnedByAll  bool
	OwnerCount  int
	Missing     int
	MemberCount int
	Owners      []ownerVM
	MoreOwners  int
}

// crewPageView is the list page.
type crewPageView struct {
	baseView
	Crew        store.Crew
	CrewID      int64
	Role        string
	IsAdmin     bool
	IsOwner     bool
	Pile        *pile.Pile
	Summary     summaryVM
	Entries     []entryVM
	Filters     []filterVM
	Sorts       []filterVM
	DataAgeText string
	MemberCount int
	EmptyTitle  string
	EmptyBody   string
	CanImport   bool
	HasAPIKey   bool
}

// filterVM is one toolbar chip.
type filterVM struct {
	Label  string
	Href   string
	Count  int
	Active bool
}

// memberVM is one row on the members page.
type memberVM struct {
	ID        int64
	Name      string
	Avatar    string
	Profile   string
	Role      string
	IsMe      bool
	Joined    string
	CanEdit   bool
	CanRemove bool
}

// inviteVM is one row in the invites table.
type inviteVM struct {
	Token   string
	URL     string
	Role    string
	Created string
	Uses    string
	Expires string
	Revoked bool
	Usable  bool
}

type membersPageView struct {
	baseView
	Crew      store.Crew
	CrewID    int64
	Role      string
	IsAdmin   bool
	IsOwner   bool
	Members   []memberVM
	Invites   []inviteVM
	NewInvite string // token of a freshly created invite, shown in the page
	BaseURL   string
}

// gamePageView is one game's detail page.
type gamePageView struct {
	baseView
	Crew      store.Crew
	CrewID    int64
	Entry     entryVM
	Game      GameFacts
	Voted     int
	Error     string
	BackHref  string
	Genres    string
	Platforms string
	Devs      string
	Pubs      string
}

// GameFacts is the long-form metadata for the detail page.
type GameFacts struct {
	Devs             string
	Pubs             string
	Genres           string
	Platforms        string
	Name             string
	Cover            string
	Description      string
	Release          string
	Short            string
	IsFree           bool
	PriceLabel       string
	MetaScore        int
	MetaURL          string
	ReviewDesc       string
	ReviewPct        int
	ReviewTotal      int
	ReviewTotalHuman string
}

type joinView struct {
	baseView
	Crew     store.Crew
	Token    string
	Members  int
	Role     string
	Already  bool
	InviteOK bool
	Reason   string
}

type errorView struct {
	baseView
	Code    int
	Title   string
	Message string
}

// --- builders ---------------------------------------------------------------

// summaryVM turns the pile summary into display strings.
func (s *Server) summaryVM(p *pile.Pile) summaryVM {
	sum := p.Summary
	cur := sum.Currency
	vm := summaryVM{
		Currency:     strings.ToUpper(cur),
		Total:        money.Format(sum.Total, cur),
		MyCost:       money.Format(sum.MyCost, cur),
		GroupCost:    money.Format(sum.GroupCost, cur),
		Ready:        sum.Ready,
		ReadyOf:      len(p.All),
		Priced:       sum.Priced,
		Free:         sum.Free,
		Unpriced:     sum.Unpriced,
		OtherMoney:   sum.OtherMoney,
		MissingForMe: sum.MissingForMe,
		Vetoed:       sum.Vetoed,
		Members:      sum.MemberCount,
	}

	var notes []string
	if sum.Free > 0 {
		notes = append(notes, fmt.Sprintf("%d free", sum.Free))
	}
	if sum.Unpriced > 0 {
		notes = append(notes, fmt.Sprintf("%d without a price in %s", sum.Unpriced, p.Crew.Country))
	}
	if sum.OtherMoney > 0 {
		notes = append(notes, fmt.Sprintf("%d priced in another currency", sum.OtherMoney))
	}
	if sum.Vetoed > 0 {
		notes = append(notes, fmt.Sprintf("%d disqualified by a hard no", sum.Vetoed))
	}
	if len(notes) > 0 {
		vm.Note = "Totals cover " + strings.Join(notes, " · ")
	}
	return vm
}

// entryVMs prepares every row for rendering.
func (s *Server) entryVMs(p *pile.Pile, entries []pile.Entry, csrf string) []entryVM {
	out := make([]entryVM, 0, len(entries))
	seenVeto := false
	for _, e := range entries {
		vm := s.entryVM(p, e, csrf)
		if vm.Vetoed && !seenVeto {
			seenVeto = true
			vm.VetoedStart = true
		}
		out = append(out, vm)
	}
	return out
}

func (s *Server) entryVM(p *pile.Pile, e pile.Entry, csrf string) entryVM {
	name := e.Game.Name
	if name == "" {
		name = fmt.Sprintf("App %d", e.AppID)
	}

	vm := entryVM{
		ID:          e.ID,
		AppID:       e.AppID,
		CrewID:      p.Crew.ID,
		CSRF:        csrf,
		CanRemove:   p.IsAdmin(),
		Name:        name,
		URL:         fmt.Sprintf("/c/%d/g/%d", p.Crew.ID, e.AppID),
		Note:        e.Note,
		Score:       e.Score(),
		ScoreSign:   sign(e.Score()),
		MyVote:      e.MyVote,
		Up:          e.Up,
		Down:        e.Down,
		Vetoed:      e.Vetoed(),
		ComingSoon:  e.Game.ComingSoon,
		Played:      e.Status == store.StatusPlayed,
		Available:   e.Available,
		OwnedByMe:   e.OwnedByMe,
		OwnedByAll:  e.OwnedByEverybody(),
		OwnerCount:  len(e.Owners),
		Missing:     e.Missing,
		MemberCount: len(p.Members),
		IsFree:      e.Free(),
	}

	vm.IsManual = e.Manual != nil
	if e.Manual != nil {
		vm.URL = fmt.Sprintf("/c/%d/entries/%d", p.Crew.ID, e.ID)
		vm.EditURL = vm.URL + "/edit"
		vm.CanEditManual = p.IsAdmin() || e.AddedBy == p.Me.ID
		vm.StoreURL = e.Manual.StoreURL
	}
	if e.Available {
		vm.Cover = e.Game.HeaderImage
		if vm.Cover == "" {
			vm.Cover = e.Game.CapsuleImage
		}
		vm.Sub = subtitle(e.Game.Developers, e.Game.ReleaseDate, e.Game.ComingSoon)
		if mc := e.Game.Metacritic; mc != nil {
			vm.HasMetaScore = true
			vm.MetaScore = mc.Score
			vm.MetaClass = scoreClass(mc.Score)
			vm.MetaURL = mc.URL
		}
	}

	if r := e.Reviews; r != nil {
		vm.ReviewDesc = r.Description
		vm.ReviewPct = e.ReviewPercent()
		vm.ReviewClass = reviewClass(vm.ReviewPct)
		vm.ReviewCount = r.TotalReviews
	}

	if e.Price != nil && e.Price.Final > 0 {
		vm.PriceLabel = money.Format(e.Price.Final, e.Price.Currency)
		vm.Discount = e.Price.DiscountPercent
		if e.CostForMe > 0 {
			vm.CostForMe = money.Format(e.CostForMe, e.Currency)
		}
	}

	if added := memberName(p.Members, e.AddedBy); added != "" {
		vm.AddedBy = added
	}

	// Ownership bubbles: a handful of initials plus a "+N".
	for i, m := range e.Owners {
		if i >= 5 {
			vm.MoreOwners = len(e.Owners) - 5
			break
		}
		vm.Owners = append(vm.Owners, ownerVM{
			Initial: initial(m.DisplayName),
			Name:    m.DisplayName,
			IsMe:    m.ID == p.Me.ID,
			Avatar:  m.AvatarURL,
		})
	}
	return vm
}

func (s *Server) filterVMs(p *pile.Pile) ([]filterVM, []filterVM) {
	counts := p.Counts()
	cid := p.Crew.ID
	q := func(filter, sortBy string) string {
		vals := url.Values{}
		if filter != "" {
			vals.Set("filter", filter)
		}
		if sortBy != "" && sortBy != pile.SortScore {
			vals.Set("sort", sortBy)
		}
		if enc := vals.Encode(); enc != "" {
			return fmt.Sprintf("/c/%d?%s", cid, enc)
		}
		return fmt.Sprintf("/c/%d", cid)
	}

	mk := func(label, value string, count int, active bool) filterVM {
		href := q(value, p.Options.Sort)
		return filterVM{Label: label, Href: href, Count: count, Active: active}
	}
	filters := []filterVM{
		mk("on the list", pile.FilterActive, counts[pile.FilterActive], p.Options.Filter == pile.FilterActive),
		mk("you'd need", pile.FilterMissing, counts[pile.FilterMissing], p.Options.Filter == pile.FilterMissing),
		mk("everyone owns", pile.FilterReady, counts[pile.FilterReady], p.Options.Filter == pile.FilterReady),
		mk("your up-votes", pile.FilterLiked, counts[pile.FilterLiked], p.Options.Filter == pile.FilterLiked),
		mk("played", pile.FilterPlayed, counts[pile.FilterPlayed], p.Options.Filter == pile.FilterPlayed),
	}

	sortLabels := []struct{ value, label string }{
		{pile.SortScore, "votes"},
		{pile.SortAdded, "newest"},
		{pile.SortPrice, "price"},
		{pile.SortMeta, "metascore"},
		{pile.SortName, "name"},
	}
	var sorts []filterVM
	for _, sl := range sortLabels {
		vals := url.Values{}
		if p.Options.Filter != "" {
			vals.Set("filter", p.Options.Filter)
		}
		if sl.value != pile.SortScore {
			vals.Set("sort", sl.value)
		}
		href := fmt.Sprintf("/c/%d", cid)
		if enc := vals.Encode(); enc != "" {
			href += "?" + enc
		}
		sorts = append(sorts, filterVM{Label: sl.label, Href: href, Active: p.Options.Sort == sl.value})
	}
	return filters, sorts
}

func dataAgeText(fetchedAt int64, now time.Time) string {
	if fetchedAt == 0 {
		return "not fetched yet"
	}
	d := now.Sub(time.Unix(fetchedAt, 0))
	switch {
	case d < 2*time.Minute:
		return "fetched just now"
	case d < time.Hour:
		return fmt.Sprintf("fetched %d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("fetched %d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("fetched %d days ago", int(d.Hours()/24))
	}
}

// --- small helpers ----------------------------------------------------------

func sign(n int) string {
	switch {
	case n > 0:
		return "pos"
	case n < 0:
		return "neg"
	}
	return "zero"
}

func scoreClass(score int) string {
	switch {
	case score >= 75:
		return "is-good"
	case score >= 50:
		return "is-mixed"
	}
	return "is-bad"
}

func reviewClass(pct int) string {
	switch {
	case pct >= 80:
		return "is-positive"
	case pct >= 60:
		return "is-mixed"
	}
	return "is-negative"
}

func initial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func subtitle(developers []string, release string, comingSoon bool) string {
	var parts []string
	if len(developers) > 0 {
		devs := developers
		if len(devs) > 2 {
			devs = devs[:2]
		}
		parts = append(parts, strings.Join(devs, ", "))
	}
	if comingSoon {
		parts = append(parts, "coming soon")
	} else if year := releaseYear(release); year != "" {
		parts = append(parts, year)
	}
	return strings.Join(parts, " · ")
}

func releaseYear(release string) string {
	for i := len(release) - 1; i >= 3; i-- {
		if release[i] == ',' || release[i] == ' ' {
			continue
		}
	}
	// Steam formats dates like "3 Dec, 2019"; grab the trailing 4-digit year.
	fields := strings.FieldsFunc(release, func(r rune) bool { return r == ' ' || r == ',' })
	for _, f := range fields {
		if len(f) == 4 && strings.Trim(f, "0123456789") == "" {
			return f
		}
	}
	return ""
}

func memberName(members []store.Member, id int64) string {
	for _, m := range members {
		if m.ID == id {
			return m.DisplayName
		}
	}
	return ""
}
