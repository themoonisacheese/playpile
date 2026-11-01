package web

import "playpile/internal/store"

type scheduleSlotVM struct{ Start, End, Time, Label, State, Symbol, Draft string }
type scheduleDayVM struct {
	Label, ShortLabel, State, EditURL, Date string
	Slots                                   []scheduleSlotVM
}

type scheduleWindowFields struct {
	Start, End string
	Overnight  bool
}
type scheduleFields struct {
	Date, Status                             string
	Windows                                  []scheduleWindowFields
	Title, ProposalStart, ProposalEnd        string
	ProposalStartInstant, ProposalEndInstant string
}
type scheduleCandidateVM struct {
	Label, Href, Detail string
	Whole               bool
}
type scheduleResponseVM struct{ Name, Response string }
type scheduleSessionVM struct {
	ID                             int64
	Title, Label, Href, MyResponse string
	CanCancel                      bool
	Responses                      []scheduleResponseVM
}
type schedulePageView struct {
	baseView
	Crew                  store.Crew
	CrewID                int64
	Zone, Duration, Error string
	ProposalOffsetNote    string
	MoreWindows           bool
	MinDate, MaxDate      string
	Fields                scheduleFields
	Days                  []scheduleDayVM
	Whole, Near           []scheduleCandidateVM
	MoreWhole, MoreNear   []scheduleCandidateVM
	Sessions              []scheduleSessionVM
}
