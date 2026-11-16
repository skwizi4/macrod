// Package model holds data types shared by sources, store and transforms.
package model

import "time"

// Observation is one value of a series on a date (time of day is ignored).
type Observation struct {
	Date  time.Time
	Value float64
}

// Release statuses (releases.status, ТЗ §8.2, §17.6).
const (
	StatusScheduled   = "scheduled"
	StatusPolling     = "polling"
	StatusDone        = "done"
	StatusTimeout     = "timeout"
	StatusRescheduled = "rescheduled"
)

// ReleaseEvent is a scheduled publication: a FRED release, an FOMC decision,
// minutes or a Beige Book.
type ReleaseEvent struct {
	ID          int64 // 0 until stored
	Source      string
	Name        string
	ScheduledAt time.Time // UTC
	Region      string
	Importance  string
	Related     Related
	Status      string
	DoneAt      time.Time // zero unless done or timed out
}

// Related is what an event publishes, stored as JSON in releases.related.
type Related struct {
	ReleaseID  int      `json:"release_id,omitempty"`  // FRED release
	Series     []string `json:"series,omitempty"`      // FRED series of the release
	Meeting    string   `json:"meeting,omitempty"`     // FOMC meeting, "2026-10-27/28"
	SEP        bool     `json:"sep,omitempty"`         // meeting with a Summary of Economic Projections
	CUSIP      string   `json:"cusip,omitempty"`       // Treasury auction
	Security   string   `json:"security,omitempty"`    // Treasury auction: "10-Year Note", "13-Week Bill"; Bund auction: "Bund"
	Tenor      string   `json:"tenor,omitempty"`       // Bund auction: tenor of the results file, "10Y"; "" for a range of terms
	Dataset    string   `json:"dataset,omitempty"`     // Treasury Fiscal Data dataset of the release
	Ticker     string   `json:"ticker,omitempty"`      // company earnings
	ReportTime string   `json:"report_time,omitempty"` // earnings: after_close | before_open | "" (not stated)
	Estimated  bool     `json:"estimated,omitempty"`   // earnings date estimated by Nasdaq/Zacks, not announced
}

// Document is an official text or a post.
type Document struct {
	ID          int64 // 0 until stored
	Source      string
	ExternalID  string
	DocType     string
	Region      string
	PublishedAt time.Time
	Author      string
	Title       string
	Body        string
	URL         string
	ContentHash string // filled by the store when empty
	IsOpinion   bool
	IsJunk      bool
	FetchedAt   time.Time

	ForwardedFrom string // Telegram forward: original post "<handle>/<id>", or the sender name without a link

	Views   int64 // Telegram views at ViewsAt; 0 = unknown
	ViewsAt time.Time

	HasPhoto bool // Telegram post with a photo; false also when unknown
}

// Auction kinds (auctions.kind): the security type, with TIPS, FRN and CMB
// told apart from plain notes, bonds and bills.
const (
	KindBill = "Bill"
	KindCMB  = "CMB"
	KindNote = "Note"
	KindBond = "Bond"
	KindTIPS = "TIPS"
	KindFRN  = "FRN"
)

// Auction is one Treasury auction (TreasuryDirect TA_WS). Numbers not yet
// published (results before the close) or not applicable have OK = false.
type Auction struct {
	Source        string    // loader: AuctionSourceTreasury (default) | finanzagentur
	Region        string    // region of the loader's agent; "" for Treasury rows
	AuctionDate   time.Time // date of the competitive close, ET calendar day (Treasury), Berlin day (Bund)
	SecurityType  string    // Bill | Note | Bond
	Kind          string    // Kind* constants
	Term          string    // as auctioned: "9-Year 11-Month"
	Tenor         string    // grouping term: original term for coupons, term for bills ("10-Year", "6-Week")
	CUSIP         string
	Reopening     bool
	ClosingTime   string // competitive close "HH:MM" ET, "" if unknown
	IssueDate     time.Time
	MaturityDate  time.Time
	OfferingAmt   Num // dollars
	HighYield     Num // coupons, %
	HighRate      Num // bills: investment (bond-equivalent) rate, %
	LowYield      Num
	MedianYield   Num
	BidToCover    Num
	TotalAccepted Num // dollars
	CompAccepted  Num // competitive awarded to indirect + direct + dealers, dollars
	IndirectPct   Num // shares of CompAccepted, %
	DirectPct     Num
	DealerPct     Num
	UpdatedAt     time.Time // TA_WS updatedTimestamp (ET), zero if none

	// Bund auctions (Finanzagentur); amounts in euros.
	AvgYield  Num    // weighted average yield, %
	BidsAmt   Num    // all bids
	Retention Num    // kept by the issuer
	Coupon    Num    // %
	Process   string // Auc | M-A (multi-ISIN)
}

// AuctionSourceTreasury is the source of the Treasury auctions, the default
// of a row that states none.
const AuctionSourceTreasury = "treasury"

// HasResults reports whether the auction results are published.
func (a Auction) HasResults() bool { return a.BidToCover.OK }

// Security is the display name of the auctioned security, also the
// calendar event name: "10-Year Note", "5-Year TIPS", "13-Week Bill".
func (a Auction) Security() string {
	if a.Kind == KindCMB {
		return "CMB"
	}
	return a.Tenor + " " + a.Kind
}

// Num is an optional number.
type Num struct {
	V  float64
	OK bool
}

// Some returns a present Num.
func Some(v float64) Num { return Num{V: v, OK: true} }

// Buyback is one Treasury buyback operation (Fiscal Data).
type Buyback struct {
	OperationDate  time.Time
	StartTime      string // "HH:MM" ET
	OperationType  string // Liquidity Support | Cash Management
	SecurityType   string // Nominal Coupons | TIPS
	MaturityBucket string
	ParOffered     Num // dollars
	ParAccepted    Num
	MaxRedeemed    Num
}

// CompanyFact is one XBRL fact of a company as filed (company_facts). Start
// is zero for instant facts. FY and FP describe the filing that carries the
// fact, not the fact's own period: a 2026 10-Q repeats 2025 values with
// fy=2026 (LOOKUP E3).
type CompanyFact struct {
	Ticker     string
	Concept    string // us-gaap tag
	Unit       string // USD, USD/shares
	Start, End time.Time
	Value      float64
	Accession  string
	Form       string
	Filed      time.Time
	FY         int
	FP         string // Q1 | Q2 | Q3 | FY
	Frame      string // CY2026Q2, when assigned
	// Member is the segment member of a fact from an XBRL instance
	// (instance_facts, phase 5d): AmazonWebServicesSegmentMember; "" for a
	// company total and for every company facts fact.
	Member string
}

// CompanyReport is one earnings report (company_reports): the 8-K with item
// 2.02. Numbers are not stored with it; they come from CompanyFact.
type CompanyReport struct {
	ID         int64 // 0 until stored
	Ticker     string
	PeriodEnd  time.Time // quarter reported: from the following 10-Q/10-K, estimated until it is filed
	Form       string    // 8-K
	ReportDate time.Time // New York date of acceptance
	Accession  string
	AcceptedAt time.Time // UTC
	Session    string    // amc | bmo | intraday
	DocumentID int64     // press release in documents; 0 if none
}
