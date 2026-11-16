// Package pluginapi is the dependency-free public SDK for executable source plugins.
package pluginapi

import "time"

const ProtocolVersion = 2
const MaxMessageSize = 8 << 20

type ReleaseState string

const (
	ReleaseUnknown   ReleaseState = ""
	ReleaseUpcoming  ReleaseState = "upcoming"
	ReleasePublished ReleaseState = "published"
)

type Series struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Cover        string       `json:"cover"`
	Description  string       `json:"description"`
	Episodes     int          `json:"episodes"`
	Season       int          `json:"season,omitempty"`
	ReleaseState ReleaseState `json:"release_state,omitempty"`
	ReleaseNote  string       `json:"release_note,omitempty"`
}
type Episode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Number int    `json:"number"`
}
type Page struct {
	Items   []Series
	Cursor  string
	HasMore bool
}
type UpcomingEntry struct {
	SeriesID  string
	Title     string
	Online    bool
	PublishAt time.Time
	Note      string
}
type Cue struct {
	Time float64 `json:"time"`
	Text string  `json:"text"`
}
type Media struct {
	URL        string
	Definition string
	Qualities  []string
	SessionID  string
}
type DiscoverOptions struct {
	Channel, Cursor, Sort string
	PageSize              int
}
type SearchOptions struct {
	Query, Cursor string
	PageSize      int
}
type Params struct {
	ID  string
	IDs []string
	DiscoverOptions
	Search    SearchOptions
	Days      int
	Quality   string
	SeriesID  string
	SessionID string
}
type DetailResult struct {
	Series   Series
	Episodes []Episode
}
