package web

import "playpile/internal/store"

type scheduleDayVM struct{ Label, State, EditURL string }
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
	Sessions              []scheduleSessionVM
}
