package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "classify.md"), []byte("## System\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `
timezone: "Europe/Bratislava"
data_dir: "/tmp/macro"
db_path: "${data_dir}/macro.db"
history_start: "2000-01-01"
export:
  exchange_dir: "${data_dir}/exchange"
  reports_dir: "${data_dir}/reports"
  weekly_cron: "0 16 * * 0"
  max_file_chars: 60000
regions:
  - id: usa
    index: "^NDX"
    yahoo_poll: "22:30"
    agents:
      - id: monetary
        series_tags: [usa_monetary, usa_fiscal, usa_master, usa_news]
        reports:
          - { report: markets, file: markets.md, title: "USA Markets", tables: { rows: [DGS10] } }
  - id: eu
    yahoo_poll: "18:00"
    agents:
      - id: monetary
        series_tags: [eu_monetary, eu_fiscal, eu_master]
        reports:
          - { report: markets, file: markets.md, title: "EU Markets", tables: { rows: ["^GDAXI"] } }
metrics:
  zscore_window_daily_weekly: 52w
  zscore_window_monthly_quarterly: 5y
fred:
  series:
    - { id: DGS10, agent: usa_fiscal, label: "UST 10Y", transforms: [level, d1w, z] }
    - { id: RPONTSYD, agent: usa_fiscal, label: "SRF", transforms: [level], flag_if_gt: 0 }
    - { id: DGS2, agent: usa_fiscal, label: "UST 2Y", transforms: [level] }
derived:
  - { name: UST_2S10S, formula: "DGS10 - DGS2", agent: usa_fiscal, unit: bp, transforms: [level, d] }
fed_texts:
  base_url: "https://fed.example"
  feeds:
    - { name: speeches, url: "https://fed.example/feeds/speeches.xml" }
  fomc_calendar: "https://fed.example/fomccalendars.htm"
  beige_book: "https://fed.example/beige-book.htm"
  poll_interval: 15m
  agent: usa_monetary
calendar:
  horizon_days: 14
  refresh_cron: "0 5 * * *"
  release_timezone: "America/New_York"
  default_release_time: "08:30"
  release_times:
    20: "16:30"
  release_importance:
    20: medium
yahoo:
  endpoint: "https://yahoo.example/v8/finance/chart/{symbol}"
  symbols:
    - { symbol: "^NDX", agent: usa_master, label: "NASDAQ 100", transforms: [level, ma50] }
    - { symbol: "^GDAXI", agent: eu_master, label: "GER40", transforms: [level] }
  fed_funds_futures:
    symbol_format: "ZQ{month}{yy}.CBT"
    months_ahead: 12
    agent: usa_monetary
treasury:
  fiscal_data_url: "https://fiscal.example/services/api/fiscal_service"
  release_calendar_url: "https://fiscal.example/services/calendar/release"
  treasurydirect_url: "https://td.example/TA_WS/securities"
  datasets:
    - { name: dts_operating_cash, agent: usa_fiscal, enabled: true }
    - { name: auctions, agent: usa_fiscal, enabled: true, security_types: ["Note", "Bond"] }
    - { name: buybacks, agent: usa_fiscal, enabled: false }
  quarterly_refunding_url: "https://treasury.example/refunding"
cftc:
  enabled: true
  endpoint: "https://cftc.example/resource/gpe5-46if.json"
  check_cron: "0 22 * * 1-5"
  contracts:
    - { name: "Nasdaq-100 E-mini", code: "209742", agent: usa_master }
    - { name: "Euro FX", code: "099741", agent: eu_master }
posts:
  poll_interval: 15m
  history_days: 30
telegram:
  enabled: true
  mode: web
  base_url: "https://t.example"
  channels:
    - { handle: macro_news, region: usa, type: news, default_tag: "" }
    - { handle: euro_rates, region: eu, type: opinion, default_tag: eu_ecb, history_days: 7 }
rss_feeds:
  - { name: compass, url: "https://compass.example/feed", region: usa, type: opinion }
classify:
  min_score: 2
  tags: [usa_monetary, usa_fiscal, usa_micro, usa_politics, energy_geopolitics, eu_ecb, eu_fiscal, eu_macro, eu_micro]
  rules:
    usa_monetary: ["FOMC", "ФРС"]
    usa_fiscal: ["Treasury"]
    usa_micro: ["earnings"]
    usa_politics: ["Congress"]
    energy_geopolitics: ["OPEC"]
    eu_ecb: ["ECB"]
    eu_fiscal: ["Bund"]
    eu_macro: ["Ifo"]
    eu_micro: ["SAP"]
  drop_if_contains: ["промокод"]
  gemini:
    enabled: true
    endpoint: "https://gemini.example/v1beta/models/{model}:generateContent"
    model: gemini-test
    prompt: "classify.md"
    batch_size: 20
    min_confidence: 0.5
    timeout: 60s
    daily_request_limit: 200
news:
  min_text_chars: 40
  signature_share: 0.5
  signature_min_posts: 4
  promo_words: ["subscribe*"]
  opinion_markers: ["I think"]
  calendar_posts: ["Основні події тижня"]
  global_max: 5
  surprise_facts:
    - { words: [cpi], series: CPIAUCSL, transform: mom, unit: "%" }
edgar:
  enabled: true
  user_agent: "macro-collector test@example.com"
  data_url: "https://data.example"
  archives_url: "https://www.example/Archives/edgar/data"
  requests_per_second: 8
  form_types: ["8-K", "10-Q", "10-K"]
  history_start: "2024-01-01"
  concepts:
    revenue: [RevenueFromContractWithCustomerExcludingAssessedTax, Revenues]
    eps: [EarningsPerShareDiluted]
    capex: [PaymentsToAcquirePropertyPlantAndEquipment]
    operating_income: [OperatingIncomeLoss]
    net_income: [NetIncomeLoss]
    gross_profit: [GrossProfit]
    da: [DepreciationDepletionAndAmortization]
    operating_cash_flow: [NetCashProvidedByUsedInOperatingActivities]
    buybacks: [PaymentsForRepurchaseOfCommonStock]
    dividends: [PaymentsOfDividends]
    cash: [CashAndCashEquivalentsAtCarryingValue]
    short_term_investments: [MarketableSecuritiesCurrent]
    debt: [LongTermDebt]
    diluted_shares: [WeightedAverageNumberOfDilutedSharesOutstanding]
    pretax_income: [IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest]
    income_tax: [IncomeTaxExpenseBenefit]
    rpo: [RevenueRemainingPerformanceObligation]
  companies:
    - { ticker: MSFT, cik: "0000789019", importance: high }
    - { ticker: AMZN, cik: "0001018724", not_paid: [dividends], concepts: { capex: [PaymentsToAcquireProductiveAssets], debt: [[LongTermDebtNoncurrent], [LongTermDebtCurrent]] } }
  capex_companies: [MSFT, AMZN]
  one_off: { rate_change_pp: 15, other_share_change_pp: 15, materiality_pct: 10 }
  macro_signals:
    enabled: true
    min_chars: 30
    max_chars: 400
    per_company: 6
    per_company_topic: 2
    max: 25
    exclude: ['non-GAAP']
    strong: ['higher memory']
    topics:
      - { name: память, patterns: ['\\b(memory|HBM)\\b'] }
  segments:
    AMZN: { revenue: RevenueFromContractWithCustomerExcludingAssessedTax, profit: OperatingIncomeLoss, members: [{ member: AmazonWebServicesSegmentMember, label: AWS }] }
  transcripts:
    enabled: true
    request_interval: 10s
    retry_every: 1h
    window_days: 4
    companies:
      - { ticker: MSFT, page: "https://ir.example/fy-{fy}/q{q}" }
earnings_calendar:
  enabled: true
  endpoint: "https://nasdaq.example/api/analyst/{ticker}/earnings-date"
  after_close_time: "16:00"
  pre_market_time: "07:00"
  unknown_time: "16:00"
`

func TestLoadMinimal(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	path := writeConfig(t, minimal)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/tmp/macro/macro.db" {
		t.Errorf("DBPath = %q", c.DBPath)
	}
	if c.Export.ExchangeDir != "/tmp/macro/exchange" || c.Export.ReportsDir != "/tmp/macro/reports" || c.Export.MaxFileChars != 60000 {
		t.Errorf("Export = %+v", c.Export)
	}
	if c.LogDir() != "/tmp/macro/logs" {
		t.Errorf("LogDir = %q", c.LogDir())
	}
	if c.Location.String() != "Europe/Bratislava" {
		t.Errorf("Location = %v", c.Location)
	}
	if got := c.History.Format("2006-01-02"); got != "2000-01-01" {
		t.Errorf("History = %s", got)
	}
	if c.Metrics.ZDailyWeekly.Days != 364 || c.Metrics.ZMonthlyQuarterly.Years != 5 {
		t.Errorf("z windows = %+v, %+v", c.Metrics.ZDailyWeekly, c.Metrics.ZMonthlyQuarterly)
	}
	if len(c.FRED.Series) != 3 {
		t.Fatalf("series = %d", len(c.FRED.Series))
	}
	if c.FRED.Series[0].FlagIfGt != nil {
		t.Error("DGS10 flag_if_gt should be unset")
	}
	if f := c.FRED.Series[1].FlagIfGt; f == nil || *f != 0 {
		t.Errorf("RPONTSYD flag_if_gt = %v, want 0", f)
	}
	if c.Derived[0].Unit != "bp" {
		t.Errorf("derived unit = %q", c.Derived[0].Unit)
	}
	if c.FedTexts.PollInterval != 15*time.Minute || len(c.FedTexts.Feeds) != 1 {
		t.Errorf("fed_texts = %+v", c.FedTexts)
	}
	if got := c.Calendar.ReleaseTime(20); got != (ClockTime{16, 30}) {
		t.Errorf("release 20 time = %+v", got)
	}
	if got := c.Calendar.ReleaseTime(10); got != (ClockTime{8, 30}) {
		t.Errorf("release 10 time (default) = %+v", got)
	}
	if c.Calendar.Importance(20) != "medium" || c.Calendar.Importance(10) != "low" {
		t.Errorf("importance 20 = %q, 10 = %q", c.Calendar.Importance(20), c.Calendar.Importance(10))
	}
	if c.Regions[0].YahooPoll != (ClockTime{22, 30}) || c.Regions[1].YahooPoll != (ClockTime{18, 0}) {
		t.Errorf("yahoo polls = %+v, %+v", c.Regions[0].YahooPoll, c.Regions[1].YahooPoll)
	}
	if us := c.Yahoo.SymbolsFor("usa"); len(us) != 1 || us[0].Symbol != "^NDX" {
		t.Errorf("usa symbols = %+v", us)
	}
	if d, ok := c.Treasury.Dataset("auctions"); !ok || len(d.SecurityTypes) != 2 {
		t.Errorf("auctions dataset = %+v, %v", d, ok)
	}
	if _, ok := c.Treasury.Dataset("buybacks"); ok {
		t.Error("disabled buybacks reported as enabled")
	}
	if _, ok := c.Treasury.Dataset("mts"); ok {
		t.Error("missing mts reported as enabled")
	}
	if k := c.CFTC.ContractsFor("usa"); len(k) != 1 || k[0].Code != "209742" {
		t.Errorf("usa contracts = %+v", k)
	}
	if c.Posts.PollInterval != 15*time.Minute {
		t.Errorf("posts.poll_interval = %v", c.Posts.PollInterval)
	}
	if ch := c.Telegram.Channels; len(ch) != 2 || ch[0].HistoryDays != 30 || ch[1].HistoryDays != 7 {
		t.Errorf("channels = %+v", ch)
	}
	if f := c.RSSFeeds; len(f) != 1 || f[0].HistoryDays != 30 {
		t.Errorf("rss_feeds = %+v", f)
	}
	if want := filepath.Join(filepath.Dir(path), "classify.md"); c.Classify.Gemini.PromptPath != want {
		t.Errorf("prompt path = %q, want %q", c.Classify.Gemini.PromptPath, want)
	}
	if got := c.EDGAR.History.Format(time.DateOnly); got != "2024-01-01" {
		t.Errorf("edgar history = %s", got)
	}
	msft, ok := c.EDGAR.Company("MSFT")
	if !ok || msft.Importance != "high" || msft.Resolved.Capex[0] != "PaymentsToAcquirePropertyPlantAndEquipment" || len(msft.Resolved.Revenue) != 2 {
		t.Errorf("MSFT = %+v, %v", msft, ok)
	}
	amzn, _ := c.EDGAR.Company("AMZN")
	if amzn.Importance != "medium" || len(amzn.Resolved.Capex) != 1 || amzn.Resolved.Capex[0] != "PaymentsToAcquireProductiveAssets" || amzn.Resolved.EPS[0] != "EarningsPerShareDiluted" {
		t.Errorf("AMZN = %+v (override capex only, default importance)", amzn)
	}
	if c.Earnings.AfterClose != (ClockTime{16, 0}) || c.Earnings.PreMarket != (ClockTime{7, 0}) {
		t.Errorf("earnings times = %+v, %+v", c.Earnings.AfterClose, c.Earnings.PreMarket)
	}
}

func TestFedFundsFuturesSymbol(t *testing.T) {
	f := FedFundsFutures{SymbolFormat: "ZQ{month}{yy}.CBT"}
	for date, want := range map[string]string{
		"2026-11-01": "ZQX26.CBT",
		"2027-01-15": "ZQF27.CBT",
		"2027-10-31": "ZQV27.CBT",
		"2030-06-01": "ZQM30.CBT",
	} {
		d, _ := time.Parse(time.DateOnly, date)
		if got := f.Symbol(d); got != want {
			t.Errorf("%s: %s, want %s", date, got, want)
		}
	}
}

// ClockTime.On keeps the wall-clock time across the DST switch: 08:30 ET is
// 12:30 UTC in summer time and 13:30 UTC after 2026-11-01.
func TestClockTimeOnDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	ct := ClockTime{8, 30}
	for date, want := range map[string]string{
		"2026-10-30": "2026-10-30T12:30:00Z",
		"2026-11-02": "2026-11-02T13:30:00Z",
	} {
		day, _ := time.Parse(time.DateOnly, date)
		if got := ct.On(day, ny).UTC().Format(time.RFC3339); got != want {
			t.Errorf("%s: %s, want %s", date, got, want)
		}
	}
}

func TestLoadDataDirFromEnv(t *testing.T) {
	t.Setenv(EnvDataDir, "/opt/macro-collector/data")
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/opt/macro-collector/data/macro.db" {
		t.Errorf("DBPath = %q", c.DBPath)
	}
}

func TestLoadDataDirPriority(t *testing.T) {
	path := writeConfig(t, minimal) // data_dir: /tmp/macro
	dir := filepath.Dir(path)
	tests := []struct {
		name, body, env, flag, want string
	}{
		{"config", minimal, "", "", "/tmp/macro"},
		{"env over config", minimal, "/srv/env", "", "/srv/env"},
		{"flag over env", minimal, "/srv/env", "/srv/flag", "/srv/flag"},
		{"relative to config dir", strings.Replace(minimal, `"/tmp/macro"`, `"./data"`, 1), "", "", filepath.Join(dir, "data")},
		{"default", strings.Replace(minimal, "data_dir: \"/tmp/macro\"\n", "", 1), "", "", filepath.Join(dir, "data")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvDataDir, tt.env)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadWithDataDir(path, tt.flag)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := filepath.Abs(tt.want)
			if c.DataDir != want || c.DBPath != filepath.Join(want, "macro.db") {
				t.Errorf("DataDir = %q, DBPath = %q, want %q", c.DataDir, c.DBPath, want)
			}
		})
	}
}

func TestLoadValidation(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	tests := []struct {
		name, from, to, wantErr string
	}{
		{"bad timezone", `"Europe/Bratislava"`, `"Mars/Base"`, "timezone"},
		{"bad history", `"2000-01-01"`, `"2000-13-01"`, "history_start"},
		{"bad z window", "52w", "52x", "zscore_window_daily_weekly"},
		{"bad agent", "agent: usa_fiscal, label: \"UST 10Y\"", "agent: usa_sport, label: \"UST 10Y\"", `unknown agent "usa_sport"`},
		{"bad transform", "[level, d1w, z]", "[level, d1w, zz]", `unknown transform "zz"`},
		{"duplicate id", "id: RPONTSYD", "id: DGS10", "duplicate id"},
		{"bad poll interval", "poll_interval: 15m", "poll_interval: 0s", "fed_texts.poll_interval"},
		{"bad fed agent", "agent: usa_monetary\ncalendar", "agent: usa_sport\ncalendar", `fed_texts: unknown agent "usa_sport"`},
		{"bad refresh cron", `"0 5 * * *"`, `"0 5 * *"`, "calendar.refresh_cron"},
		{"bad release timezone", `"America/New_York"`, `"America/Gotham"`, "calendar.release_timezone"},
		{"bad default time", `"08:30"`, `"8.30"`, "calendar.default_release_time"},
		{"bad release time", `"16:30"`, `"25:00"`, "calendar.release_times[20]"},
		{"bad derived transform", "transforms: [level, d] }", "transforms: [level, dd] }", `derived[0] UST_2S10S: unknown transform "dd"`},
		{"bad importance", "20: medium", "20: urgent", "calendar.release_importance[20]"},
		{"bad weekly cron", `"0 16 * * 0"`, `"0 16 * *"`, "export.weekly_cron"},
		{"no reports dir", `reports_dir: "${data_dir}/reports"`, `reports_dir: ""`, "export.reports_dir"},
		{"bad yahoo endpoint", "chart/{symbol}", "chart/", "yahoo.endpoint"},
		{"bad yahoo poll", `"22:30"`, `"22.30"`, "regions[0].yahoo_poll"},
		{"duplicate yahoo symbol", `symbol: "^GDAXI"`, `symbol: "^NDX"`, "duplicate symbol"},
		{"bad yahoo transform", "[level, ma50]", "[level, ma51]", `yahoo.symbols[0] ^NDX: unknown transform "ma51"`},
		{"bad zq format", `"ZQ{month}{yy}.CBT"`, `"ZQ{month}.CBT"`, "symbol_format"},
		{"bad zq months", "months_ahead: 12", "months_ahead: 0", "months_ahead"},
		{"bad treasury url", `"https://td.example/TA_WS/securities"`, `""`, "treasury.treasurydirect_url"},
		{"unknown dataset", "name: dts_operating_cash", "name: dts", "unknown dataset"},
		{"bad security type", `["Note", "Bond"]`, `["Note", "TIPS"]`, `unknown security type "TIPS"`},
		{"bad cftc cron", `"0 22 * * 1-5"`, `"0 22 * *"`, "cftc.check_cron"},
		{"duplicate cftc code", `code: "099741"`, `code: "209742"`, "duplicate code"},
		{"missing cftc code", `code: "099741", `, "", "name and code are required"},
		{"bad posts interval", "poll_interval: 15m\n  history_days", "poll_interval: 0s\n  history_days", "posts.poll_interval"},
		{"mtproto", "mode: web", "mode: mtproto", "only web is supported"},
		{"placeholder profile", `type: news, default_tag: "" }`, `type: news, default_tag: "", profile: "[[FILL]]" }`, "macro_news: profile is a placeholder"},
		{"negative opinions_full", `type: news, default_tag: "" }`, `type: news, default_tag: "", opinions_full: -1 }`, "macro_news: opinions_full must not be negative"},
		{"bad handle", "handle: macro_news", "handle: t.me/macro_news", "telegram.channels[0] t.me/macro_news: handle"},
		{"duplicate handle", "handle: euro_rates", "handle: Macro_News", "duplicate handle"},
		{"bad channel region", "region: eu, type: opinion", "region: asia, type: opinion", `region "asia"`},
		{"bad channel type", "region: usa, type: news", "region: usa, type: rumours", `type "rumours"`},
		{"bad default tag", "default_tag: eu_ecb", "default_tag: crypto", `default_tag "crypto"`},
		{"placeholder handle", "handle: macro_news", `handle: "[[FILL]]"`, "telegram.channels[0] [[FILL]]: handle"},
		{"bad feed url", `"https://compass.example/feed"`, `"compass.example/feed"`, "rss_feeds[0] compass url"},
		{"bad feed name", "name: compass", "name: Macro Letter", "name must be lowercase"},
		{"unknown tag", `eu_micro: ["SAP"]`, `crypto: ["BTC"]`, `unknown tag "crypto"`},
		{"missing tag", `eu_micro: ["SAP"]`, "", "no keywords for tag eu_micro"},
		{"placeholder keyword", `["earnings"]`, `["[[FILL]]"]`, "placeholder keyword"},
		{"bad gemini endpoint", "models/{model}:generateContent", "models/gemini:generateContent", "must contain {model}"},
		{"bad min confidence", "min_confidence: 0.5", "min_confidence: 1.5", "classify.gemini.min_confidence"},
		{"missing prompt", `prompt: "classify.md"`, `prompt: "nope.md"`, "classify.gemini.prompt"},
		{"edgar user agent without email", `"macro-collector test@example.com"`, `"macro-collector [[FILL: email]]"`, "edgar.user_agent"},
		{"edgar rate above sec limit", "requests_per_second: 8", "requests_per_second: 20", "edgar.requests_per_second"},
		{"edgar unknown form", `["8-K", "10-Q", "10-K"]`, `["8-K", "S-1"]`, `unknown form "S-1"`},
		{"edgar bad history", `history_start: "2024-01-01"`, `history_start: "2024"`, "edgar.history_start"},
		{"edgar short cik", `cik: "0000789019"`, `cik: "789019"`, "edgar.companies[0] MSFT: cik"},
		{"edgar duplicate ticker", "ticker: AMZN", "ticker: MSFT", "duplicate ticker"},
		{"edgar lowercase ticker", "ticker: AMZN", "ticker: amzn", "ticker must be uppercase"},
		{"edgar bad importance", "importance: high }", "importance: urgent }", `importance "urgent"`},
		{"edgar bad concept", "[PaymentsToAcquireProductiveAssets]", "[us-gaap:Capex]", `bad XBRL tag "us-gaap:Capex"`},
		{"edgar no default eps", "eps: [EarningsPerShareDiluted]", "eps: []", "edgar.concepts.eps is empty"},
		{"edgar no default debt", "debt: [LongTermDebt]", "debt: []", "edgar.concepts.debt is empty"},
		{"edgar empty debt component", "debt: [[LongTermDebtNoncurrent], [LongTermDebtCurrent]]", "debt: [[LongTermDebtNoncurrent], []]", "debt: empty component"},
		{"edgar bad component tag", "[LongTermDebtCurrent]] }", "[us-gaap:Debt]] }", `bad XBRL tag "us-gaap:Debt"`},
		{"edgar bad not_paid", "not_paid: [dividends]", "not_paid: [interest]", `not_paid "interest"`},
		{"transcript too fast", "request_interval: 10s", "request_interval: 1s", "edgar.transcripts.request_interval"},
		{"transcript unknown company", "{ ticker: MSFT, page:", "{ ticker: AAPL, page:", "AAPL: not in edgar.companies"},
		{"transcript page without quarter", "fy-{fy}/q{q}", "fy-{fy}/latest", "must contain {fy} and {q}"},
		{"one-off threshold", "materiality_pct: 10 }", "materiality_pct: 0 }", "edgar.one_off"},
		{"signal bad pattern", "(memory|HBM)", "(memory|HBM", "edgar.macro_signals.topics[0]"},
		{"signal limits", "per_company_topic: 2", "per_company_topic: 7", "per_company_topic 7"},
		{"guidance company unknown", "capex_companies: [MSFT, AMZN]", "capex_companies: [MSFT, AMZN]\n  capex_guidance: [{ ticker: GOOGL, year: 2026, low: 1, high: 2, given: 2026-10-28, source: x }]", "edgar.capex_guidance[0] GOOGL: not in edgar.capex_companies"},
		{"guidance range", "capex_companies: [MSFT, AMZN]", "capex_companies: [MSFT, AMZN]\n  capex_guidance: [{ ticker: MSFT, year: 2026, low: 5, high: 2, given: 2026-10-28, source: x }]", "0 < low ≤ high"},
		{"guidance ticker not capex", "capex_companies: [MSFT, AMZN]", "capex_companies: [MSFT, AMZN]\n  guidance_tickers: [GOOGL]", "edgar.guidance_tickers: GOOGL is not in edgar.capex_companies"},
		{"segment with prefix", "member: AmazonWebServicesSegmentMember", "member: amzn:AmazonWebServicesSegmentMember", "want a local name"},
		{"segment company unknown", "    AMZN: { revenue: Revenue", "    NFLX: { revenue: Revenue", "edgar.segments.NFLX: not in edgar.companies"},
		{"edgar capex company unknown", "capex_companies: [MSFT, AMZN]", "capex_companies: [MSFT, META]", "META is not in edgar.companies"},
		{"earnings endpoint", "analyst/{ticker}/earnings-date", "analyst/MSFT/earnings-date", "must contain {ticker}"},
		{"surprise fact transform", "transform: mom, unit", "transform: m/m, unit", `news.surprise_facts[0] CPIAUCSL: transform "m/m"`},
		{"surprise fact unit", `unit: "%" }`, `unit: "pct" }`, `unit "pct"`},
		{"earnings bad time", `after_close_time: "16:00"`, `after_close_time: "4pm"`, "earnings_calendar.after_close_time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(minimal, tt.from, tt.to, 1)
			if body == minimal {
				t.Fatalf("replacement %q not applied", tt.from)
			}
			_, err := Load(writeConfig(t, body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// config.example.yaml (committed) and the personal config.yaml (not in the
// repo, D17; checked when present) must always load: they carry [[FILL]] and
// [[LOOKUP]] placeholders in sections that later phases will parse.
func TestCommittedConfigsLoad(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	for _, name := range []string{"config.example.yaml", "config.yaml"} {
		t.Run(name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join("..", "..", name)); name == "config.yaml" && os.IsNotExist(err) {
				t.Skip("no personal config.yaml")
			}
			c, err := Load(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			if n := len(c.FRED.Series); n != 65 {
				t.Errorf("fred series = %d, want 65", n)
			}
			if n := len(c.Calendar.Times); n != 19 {
				t.Errorf("calendar release times = %d, want 19", n)
			}
			if n := len(c.Calendar.ReleaseImportance); n != 12 {
				t.Errorf("calendar release importance = %d, want 12", n)
			}
			if n := len(c.Yahoo.SymbolsFor("usa")); n != 23 {
				t.Errorf("usa yahoo symbols = %d, want 23", n)
			}
			nov, _ := time.Parse(time.DateOnly, "2026-11-01")
			if s := c.Yahoo.FedFundsFutures.Symbol(nov); s != "ZQX26.CBT" {
				t.Errorf("ZQ symbol = %s", s)
			}
			if n := len(c.Treasury.Datasets); n != 6 {
				t.Errorf("treasury datasets = %d, want 6", n)
			}
			// With the region eu (the migrated config.yaml, phase 11) Euro FX
			// belongs to eu_master: markets_eu.md shows its COT.
			usaContracts, euContracts := 3, 0
			if _, ok := c.Region("eu"); ok {
				usaContracts, euContracts = 2, 1
			}
			if n := len(c.CFTC.ContractsFor("usa")); n != usaContracts {
				t.Errorf("usa cftc contracts = %d, want %d", n, usaContracts)
			}
			if n := len(c.CFTC.ContractsFor("eu")); n != euContracts {
				t.Errorf("eu cftc contracts = %d, want %d", n, euContracts)
			}
			// The example ships without channels (phase 15: no personal reading
			// list in the public repo); a personal config has its own.
			if n := len(c.Telegram.Channels) + len(c.RSSFeeds); name == "config.example.yaml" && (n != 0 || c.Telegram.Enabled) {
				t.Errorf("example post sources = %d, telegram enabled %v; want none", n, c.Telegram.Enabled)
			}
			if c.Classify.Gemini.Model == "" || (name == "config.example.yaml" && c.Classify.Gemini.Enabled) {
				t.Errorf("gemini = %+v (the example ships with it off, like telegram)", c.Classify.Gemini)
			}
			if n := len(c.EDGAR.Companies); !c.EDGAR.Enabled || n != 8 || len(c.EDGAR.CapexCompanies) != 4 {
				t.Errorf("edgar: enabled %v, companies %d, capex %v", c.EDGAR.Enabled, n, c.EDGAR.CapexCompanies)
			}
			if nvda, _ := c.EDGAR.Company("NVDA"); nvda.Resolved.Capex[0] != "PaymentsToAcquireProductiveAssets" || nvda.Resolved.Revenue[0] != "Revenues" {
				t.Errorf("NVDA concepts = %+v", nvda.Resolved)
			}
			if avgo, _ := c.EDGAR.Company("AVGO"); len(avgo.Resolved.DA) != 2 || avgo.Resolved.Debt[0][1] != "LongTermDebtAndCapitalLeaseObligations" ||
				avgo.Resolved.OperatingIncome[0] != "OperatingIncomeLoss" {
				t.Errorf("AVGO concepts = %+v", avgo.Resolved)
			}
			if amzn, _ := c.EDGAR.Company("AMZN"); len(amzn.NotPaid) != 2 || len(amzn.Resolved.CostOfRevenue) != 0 {
				t.Errorf("AMZN = %+v", amzn)
			}
			if s, ok := c.EDGAR.Transcripts.Transcript("GOOGL"); !ok || s.Feed == "" || c.EDGAR.Transcripts.Interval != 10*time.Second {
				t.Errorf("transcripts = %+v", c.EDGAR.Transcripts)
			}
			if m := c.EDGAR.MacroSignals; len(m.Topics) != 7 || !m.Strongs.MatchString("deliveries were delayed") || !m.Topics[1].Match.MatchString("given the supply‑constraint environment") ||
				!m.Excluded.MatchString("an integrated supply chain") || m.Topics[6].Match.MatchString("agentic hiring") {
				t.Errorf("macro signals = %+v", m)
			}
			if amzn, _ := c.EDGAR.Company("AMZN"); !strings.HasSuffix(amzn.Resolved.PretaxIncome[0], "EquityMethodInvestments") {
				t.Errorf("AMZN pretax = %v", amzn.Resolved.PretaxIncome)
			}
			if g := c.EDGAR.CapexGuidance; len(g) != 1 || g[0].Ticker != "GOOGL" || g[0].Low != 195 || g[0].High != 205 {
				t.Errorf("capex guidance = %+v", g)
			}
			if tsla := c.EDGAR.Segments["TSLA"]; len(c.EDGAR.Segments) != 8 || !tsla.Gross || len(c.EDGAR.Segments["AAPL"].Members) != 5 {
				t.Errorf("segments = %+v", c.EDGAR.Segments)
			}
			if !c.Earnings.Enabled {
				t.Error("earnings_calendar disabled")
			}
		})
	}
}

func TestRegionsValidation(t *testing.T) {
	report := `{ report: markets, file: markets.md, title: "USA Markets", tables: { rows: [DGS10] } }`
	for _, tt := range []struct{ name, old, new, want string }{
		{"misspelt table", `tables: { rows: [DGS10] }`, `tables: { rows: [DGS10], infaltion: [DGS10] }`, `unknown table "infaltion" (want one of rows)`},
		{"missing table", `tables: { rows: [DGS10] }`, `tables: {}`, `table "rows" is missing`},
		{"misspelt field", `tables: { rows: [DGS10] }`, `tabels: { rows: [DGS10] }`, "field tabels not found"},
		{"unknown series", `rows: [DGS10]`, `rows: [NOPE]`, `"NOPE" is not a configured series`},
		{"unknown kind", `report: markets, file: markets.md, title: "USA`, `report: nope, file: markets.md, title: "USA`, `unknown report "nope"`},
		{"uppercase id", "- id: usa\n    index", "- id: USA\n    index", `id "USA"`},
		{"unknown text", "      - id: monetary\n        series_tags: [usa_monetary,", "      - id: monetary\n        texts: [nope]\n        series_tags: [usa_monetary,", `unknown text source "nope"`},
		{"unknown company", "      - id: monetary\n        series_tags: [usa_monetary,", "      - id: monetary\n        companies: [NOPE]\n        series_tags: [usa_monetary,", `company "NOPE"`},
		{"unknown tag on a series", "agent: usa_fiscal, label: \"UST 10Y\"", "agent: nobody, label: \"UST 10Y\"", `unknown agent "nobody"`},
		{"path in file", "file: markets.md, title: \"USA", "file: a/markets.md, title: \"USA", "want a file name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(minimal, report) {
				t.Fatal("fixture changed")
			}
			_, err := Load(writeConfig(t, strings.Replace(minimal, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRegionsSectionRequired(t *testing.T) {
	from, to := strings.Index(minimal, "regions:\n"), strings.Index(minimal, "metrics:\n")
	_, err := Load(writeConfig(t, minimal[:from]+minimal[to:]))
	if err == nil || !strings.Contains(err.Error(), "section regions is missing") {
		t.Errorf("err = %v", err)
	}
}

func TestSelectRegions(t *testing.T) {
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"usa", "USA", "Usa"} {
		rs, err := c.SelectRegions(v)
		if err != nil || len(rs) != 1 || rs[0].ID != "usa" {
			t.Errorf("%s: %v, %v", v, rs, err)
		}
	}
	if rs, err := c.SelectRegions("ALL"); err != nil || len(rs) != 2 {
		t.Errorf("all: %v, %v", rs, err)
	}
	if _, err := c.SelectRegions("mars"); err == nil || !strings.Contains(err.Error(), "usa, eu or all") {
		t.Errorf("mars: %v", err)
	}
}
