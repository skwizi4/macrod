package export

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// USA News (§12.4, rework of 2026-11-03): posts are never cut; the file is
// kept small only by dropping noise whole. Topics, in output order.
const (
	topicFed = iota
	topicFiscal
	topicPolitics
	topicEnergy
	topicAI
	topicEarnings
	topicMarket
	topicEurope
	topicGlobal

	topicOtherRegion = -1 // only tags of another region: its news (§11.6)
	topicNone        = -2 // no tags: uncategorized.md
)

var newsTopics = []string{
	"ФРС и ставки", "Фискал и госдолг", "Политика США", "Энергия и геополитика",
	"AI и техсектор", "Корпоративные отчёты", "Рынок и позиционирование", "Европа и ЕЦБ", "Глобально",
}

// articleChars bounds RSS articles in the file (§12.4 example: the first
// 2000 characters); Telegram posts go in full.
const articleChars = 2000

// Overview sizes.
const (
	newsTopPosts      = 5
	newsTopPerChannel = 2
	firstPhraseChars  = 200
	channelWarnShare  = 0.5
	resonanceWeeks    = 4 // the channel's median views are taken over this many weeks
	resonanceDecimals = 1
)

// dropReason is why a USA post is not in the file, in the order of
// "Отфильтровано".
type dropReason int

const (
	dropJunk dropReason = iota
	dropDuplicate
	dropImageOnly
	dropCalendar
	dropAnnounce
	dropCaption
	dropProduct
	dropOtherRegion
	dropGlobal
	dropSize
	dropUncategorized
	dropReasons
)

var dropTitles = [dropReasons]string{
	"Мусор (классификатор)", "Дубли и репосты", "Картинка без текста", "Анонсы календаря",
	"Анонсы, отсчёты, «скоро выйдет»", "Подписи к картинкам", "Продуктовые новости",
	"Только Европа (для EU News)", "«Глобально» сверх лимита", "Лимит размера файла", "Без темы (uncategorized.md)",
}

// dropNames are the codes of the reasons in the JSON.
var dropNames = [dropReasons]string{
	"junk", "duplicate", "image_only", "calendar", "announce", "caption", "product", "eu_only", "global", "size", "uncategorized",
}

// dropTitle is the heading of a reason, by its code.
func dropTitle(code string) string {
	for i, n := range dropNames {
		if n == code {
			return dropTitles[i]
		}
	}
	return code
}

// newsEntry is one exported post.
type newsEntry struct {
	post    store.WeekPost
	topic   int
	text    string
	opinion bool
	also    []string        // authors of merged reposts
	score   float64         // score of the main tag
	res     transform.Value // views over the channel's median views (resonance)
	brief   bool            // an opinion over news.opinions_per_channel: one line
	dropped bool            // dropped by the size limit
}

// newsFiltered is a post left out of the file, for news_filtered.md.
type newsFiltered struct {
	post   store.WeekPost
	reason dropReason
	text   string
	dupOf  string // URL of the canonical post of a duplicate
}

// newsDoc holds everything one rendering of USA News needs.
type newsDoc struct {
	entries   []*newsEntry
	filtered  []newsFiltered
	surprises []newsSurprise
	mentions  NewsMentions
	total     int
	pending   int
}

// news renders usa/news/current_week.md, the archive-only
// news_filtered.md and uncategorized.md of the week.
func (w *week) news(ctx context.Context) (news, filtered, uncategorized string, err error) {
	posts, err := w.st.PostsBetween(ctx, w.from, w.to)
	if err != nil {
		return "", "", "", err
	}
	prev, err := w.st.PostsBetween(ctx, w.from.AddDate(0, 0, -7), w.from)
	if err != nil {
		return "", "", "", err
	}
	history, err := w.st.PostsBetween(ctx, w.from.AddDate(0, 0, -7*(resonanceWeeks-1)), w.to)
	if err != nil {
		return "", "", "", err
	}
	var d newsDoc
	d.mentions = w.newsMentions(posts, prev)

	cl := newNewsCleaner(w.cfg.News, w.newsConfig(), w.cfg.Classify.Rules, posts)
	byID := make(map[int64]*newsEntry)
	var global, uncat []*newsEntry
	for _, p := range posts {
		if !w.inNews(p) {
			continue
		}
		d.total++
		switch {
		case p.DupOf != 0:
			continue // attached to its canonical post below
		case !p.Classified:
			d.pending++
			continue
		case p.IsJunk:
			d.filtered = append(d.filtered, newsFiltered{post: p, reason: dropJunk, text: postText(p)})
			continue
		}
		body := cl.clean(p)
		for _, s := range append(extractSurprises(body), extractForecasts(body)...) {
			d.surprises = append(d.surprises, newsSurprise{post: p, surprise: s})
		}
		p.Body = body
		topic := cl.topic(p, body)
		if r, ok := cl.drop(p, body, topic); ok {
			d.filtered = append(d.filtered, newsFiltered{post: p, reason: r, text: postText(p)})
			continue
		}
		e := &newsEntry{post: p, topic: topic, text: postText(p), opinion: cl.isOpinion(p)}
		switch topic {
		case topicOtherRegion:
			d.filtered = append(d.filtered, newsFiltered{post: p, reason: dropOtherRegion, text: e.text})
			continue
		case topicNone:
			d.filtered = append(d.filtered, newsFiltered{post: p, reason: dropUncategorized, text: e.text})
			uncat = append(uncat, e)
			continue
		case topicGlobal:
			global = append(global, e)
		}
		if len(p.Tags) > 0 {
			e.score = p.Tags[0].Score
		}
		byID[p.ID] = e
		d.entries = append(d.entries, e)
	}
	// "Глобально" keeps the global_max posts with the highest score.
	slices.SortStableFunc(global, func(a, b *newsEntry) int { return cmp.Compare(b.score, a.score) })
	if len(global) > w.cfg.News.GlobalMax {
		for _, e := range global[w.cfg.News.GlobalMax:] {
			d.filtered = append(d.filtered, newsFiltered{post: e.post, reason: dropGlobal, text: e.text})
			delete(byID, e.post.ID)
		}
		over := global[w.cfg.News.GlobalMax:]
		d.entries = slices.DeleteFunc(d.entries, func(e *newsEntry) bool { return slices.Contains(over, e) })
	}
	urls := make(map[int64]string, len(posts))
	for _, p := range posts {
		urls[p.ID] = p.URL
	}
	for _, p := range posts {
		if !w.inNews(p) || p.DupOf == 0 {
			continue
		}
		d.filtered = append(d.filtered, newsFiltered{post: p, reason: dropDuplicate, dupOf: urls[p.DupOf]})
		// A repost of a post exported in an earlier week is not repeated.
		if e, ok := byID[p.DupOf]; ok && p.Author != e.post.Author && !slices.Contains(e.also, p.Author) {
			e.also = append(e.also, p.Author)
		}
	}
	if d.pending > 0 {
		w.logWarn("posts not classified yet are left out of USA News", "posts", d.pending)
	}
	channelViews := make(map[string][]int64)
	for _, p := range history {
		if p.DupOf == 0 {
			channelViews[p.Source] = append(channelViews[p.Source], p.Views)
		}
	}
	for _, e := range d.entries {
		e.res = transform.Resonance(e.post.Views, channelViews[e.post.Source])
	}
	w.briefOpinions(d.entries)
	if err := w.resolveSurprises(ctx, &d); err != nil {
		return "", "", "", err
	}

	m := w.newsReport(&d)
	body, err := w.render("news", m)
	if err != nil {
		return "", "", "", err
	}
	if limit := w.cfg.Export.MaxFileChars; limit > 0 && len([]rune(body)) > limit {
		if m, err = w.fitNews(&d, limit); err != nil {
			return "", "", "", err
		}
	}
	if news, err = w.report(w.path(), config.ReportNews, m); err != nil {
		return "", "", "", err
	}
	if filtered, err = w.archiveReport(w.rep.FilteredFile, "news_filtered", w.filteredReport(&d)); err != nil {
		return "", "", "", err
	}
	if uncategorized, err = w.archiveReport(w.rep.UncategorizedFile, "uncategorized", w.uncategorizedReport(uncat)); err != nil {
		return "", "", "", err
	}
	return news, filtered, uncategorized, nil
}

// byResonance orders posts loudest first: by resonance, posts without one
// after those with it (by views), then by time.
func byResonance(a, b *newsEntry) int {
	switch {
	case a.res.OK && !b.res.OK:
		return -1
	case !a.res.OK && b.res.OK:
		return 1
	case a.res.OK && a.res.V != b.res.V:
		return cmp.Compare(b.res.V, a.res.V)
	}
	if c := cmp.Compare(b.post.Views, a.post.Views); c != 0 {
		return c
	}
	return a.post.PublishedAt.Compare(b.post.PublishedAt)
}

// briefOpinions keeps news.opinions_per_channel opinions of each channel
// (a channel's opinions_full, if set) in full, the loudest by resonance;
// the rest go as one line each. A post is never cut: it is chosen whole or
// pointed to.
func (w *week) briefOpinions(entries []*newsEntry) {
	limits := make(map[string]int)
	for _, ch := range w.cfg.Telegram.Channels {
		if ch.OpinionsFull > 0 {
			limits["telegram:"+ch.Handle] = ch.OpinionsFull
		}
	}
	bySource := make(map[string][]*newsEntry)
	for _, e := range entries {
		if e.opinion {
			bySource[e.post.Source] = append(bySource[e.post.Source], e)
		}
	}
	for src, list := range bySource {
		limit := w.cfg.News.OpinionsPerChannel
		if l, ok := limits[src]; ok {
			limit = l
		}
		if limit <= 0 {
			continue
		}
		slices.SortStableFunc(list, byResonance)
		for _, e := range list[min(limit, len(list)):] {
			e.brief = true
		}
	}
}

// fitNews applies §12.6: news posts with the lowest resonance go whole, one
// at a time (without a resonance first, then fewer views, older first).
// Opinions, the overview and the surprises are never dropped or cut.
func (w *week) fitNews(d *newsDoc, limit int) (*NewsReport, error) {
	var news []*newsEntry
	for _, e := range d.entries {
		if !e.opinion {
			news = append(news, e)
		}
	}
	slices.SortStableFunc(news, func(a, b *newsEntry) int {
		switch {
		case a.res.OK != b.res.OK:
			if !a.res.OK {
				return -1
			}
			return 1
		case a.res.OK && a.res.V != b.res.V:
			return cmp.Compare(a.res.V, b.res.V)
		}
		if c := cmp.Compare(a.post.Views, b.post.Views); c != 0 {
			return c
		}
		return a.post.PublishedAt.Compare(b.post.PublishedAt)
	})
	m := w.newsReport(d)
	size := 0
	for _, e := range news {
		e.dropped = true
		m = w.newsReport(d)
		body, err := w.render("news", m)
		if err != nil {
			return nil, err
		}
		if size = len([]rune(body)); size <= limit {
			return m, nil
		}
	}
	if size == 0 {
		body, err := w.render("news", m)
		if err != nil {
			return nil, err
		}
		size = len([]rune(body))
	}
	w.logWarn("USA News over the size limit with all news posts dropped", "chars", size, "limit", limit)
	return m, nil
}

// NewsPost is a post in full; Resonance is null without one.
type NewsPost struct {
	Published time.Time `json:"published"`
	Author    string    `json:"author"`
	Views     int64     `json:"views"`
	Resonance Quantity  `json:"resonance"`
	Text      string    `json:"text"`
	Also      []string  `json:"also"`
	URL       string    `json:"url"`
}

// NewsStats counts the posts of the file (its front matter).
type NewsStats struct {
	PostsTotal       int `json:"posts_total"`
	JunkDropped      int `json:"junk_dropped"`
	DuplicatesMerged int `json:"duplicates_merged"`
	ImageOnlyDropped int `json:"image_only_dropped"`
	CalendarDropped  int `json:"calendar_dropped"`
	AnnounceDropped  int `json:"announce_dropped"`
	CaptionDropped   int `json:"caption_dropped"`
	ProductDropped   int `json:"product_dropped"`
	OtherRegion      int `json:"eu_only"` // the contract's name for posts of another region
	GlobalDropped    int `json:"global_dropped"`
	SizeDropped      int `json:"size_dropped"`
	Uncategorized    int `json:"uncategorized"`
	Opinions         int `json:"opinions"`
	OpinionsBrief    int `json:"opinions_brief"`
	News             int `json:"news"`
}

// TopPost is a row of the loudest posts.
type TopPost struct {
	Resonance   Quantity `json:"resonance"`
	Views       int64    `json:"views"`
	Author      string   `json:"author"`
	FirstPhrase string   `json:"first_phrase"`
	URL         string   `json:"url"`
}

// NewsChannel is a channel's share of the posts in the file; Warn: it is
// more than half of them.
type NewsChannel struct {
	Name    string   `json:"name"`
	Posts   int      `json:"posts"`
	Share   Quantity `json:"share"`
	Profile string   `json:"profile"`
	Warn    bool     `json:"warn"`
}

// NewsSurpriseRow is a row of "Сюрпризы недели" (D11): numbers as written;
// FactSource is empty for a fact taken from the post.
type NewsSurpriseRow struct {
	Date       Day    `json:"date"`
	Label      string `json:"label"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	FactSource string `json:"fact_source"`
	Diff       string `json:"diff"`
	URL        string `json:"url"`
}

// TopicGroup is the posts of one topic.
type TopicGroup struct {
	Title string     `json:"title"`
	Posts []NewsPost `json:"posts"`
}

// BriefPost is an opinion over the per-channel limit.
type BriefPost struct {
	Date        Day    `json:"date"`
	Author      string `json:"author"`
	Topic       string `json:"topic"`
	FirstPhrase string `json:"first_phrase"`
	URL         string `json:"url"`
}

// DropCount is how many posts a reason left out.
type DropCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// NewsReport is usa/news/current_week.md.
type NewsReport struct {
	Header         Header            `json:"header"`
	Stats          NewsStats         `json:"stats"`
	Mentions       NewsMentions      `json:"mentions"`
	ResonanceWeeks int               `json:"resonance_weeks"`
	TopPerChannel  int               `json:"top_per_channel"`
	Top            []TopPost         `json:"top"`
	Channels       []NewsChannel     `json:"channels"`
	Surprises      []NewsSurpriseRow `json:"surprises"`
	Opinions       []TopicGroup      `json:"opinions"`
	Brief          []BriefPost       `json:"brief"`
	News           []TopicGroup      `json:"news"`
	Drops          []DropCount       `json:"drops"`
}

func (w *week) newsPost(p store.WeekPost, text string, also []string, res transform.Value) NewsPost {
	return NewsPost{Published: p.PublishedAt.In(w.loc), Author: p.Author, Views: p.Views, Resonance: qty(res, unitTimes), Text: text, Also: append([]string{}, also...), URL: p.URL}
}

func (w *week) newsReport(d *newsDoc) *NewsReport {
	var counts [dropReasons]int
	for _, f := range d.filtered {
		counts[f.reason]++
	}
	opinions, brief, news := 0, 0, 0
	for _, e := range d.entries {
		switch {
		case e.dropped:
			counts[dropSize]++
		case e.brief:
			brief++
		case e.opinion:
			opinions++
		default:
			news++
		}
	}
	var sources []string
	if w.cfg.Telegram.Enabled && len(w.cfg.Telegram.Channels) > 0 {
		sources = append(sources, "telegram")
	}
	if len(w.cfg.RSSFeeds) > 0 {
		sources = append(sources, "rss")
	}
	m := &NewsReport{
		Header: w.newHeader("", w.title(), sources, nil, w.from, w.to),
		Stats: NewsStats{PostsTotal: d.total, JunkDropped: counts[dropJunk], DuplicatesMerged: counts[dropDuplicate], ImageOnlyDropped: counts[dropImageOnly],
			CalendarDropped: counts[dropCalendar], AnnounceDropped: counts[dropAnnounce], CaptionDropped: counts[dropCaption], ProductDropped: counts[dropProduct],
			OtherRegion: counts[dropOtherRegion], GlobalDropped: counts[dropGlobal], SizeDropped: counts[dropSize], Uncategorized: counts[dropUncategorized],
			Opinions: opinions, OpinionsBrief: brief, News: news},
		Mentions: d.mentions, ResonanceWeeks: resonanceWeeks, TopPerChannel: newsTopPerChannel, Drops: []DropCount{},
		Surprises: w.surpriseRows(d.surprises), Opinions: w.topicGroups(d.entries, true), Brief: w.briefPosts(d.entries), News: w.topicGroups(d.entries, false),
	}
	m.Top, m.Channels = w.overview(d)
	for r := range dropReasons {
		if counts[r] > 0 {
			m.Drops = append(m.Drops, DropCount{Reason: dropNames[r], Count: counts[r]})
		}
	}
	return m
}

// overview is the rest of "0. Обзор": the loudest posts by resonance and
// the channels' shares of the posts with their profiles. It counts the
// posts that passed the filters, before the size limit, so it does not
// change with the limit.
func (w *week) overview(d *newsDoc) ([]TopPost, []NewsChannel) {
	var top []*newsEntry
	byChannel := make(map[string]int)
	source := make(map[string]string)
	var channels []string
	for _, e := range d.entries {
		if e.res.OK {
			top = append(top, e)
		}
		if byChannel[e.post.Author] == 0 {
			channels = append(channels, e.post.Author)
			source[e.post.Author] = e.post.Source
		}
		byChannel[e.post.Author]++
	}
	slices.SortStableFunc(top, byResonance)
	rows := []TopPost{}
	per := make(map[string]int)
	for _, e := range top {
		if len(rows) == newsTopPosts {
			break
		}
		if per[e.post.Source] == newsTopPerChannel {
			continue
		}
		per[e.post.Source]++
		rows = append(rows, TopPost{Resonance: qty(e.res, unitTimes), Views: e.post.Views, Author: e.post.Author, FirstPhrase: firstPhrase(e), URL: e.post.URL})
	}
	profiles := w.sourceProfiles()
	slices.SortStableFunc(channels, func(a, c string) int { return cmp.Compare(byChannel[c], byChannel[a]) })
	out := []NewsChannel{}
	for _, ch := range channels {
		share := float64(byChannel[ch]) / float64(len(d.entries))
		out = append(out, NewsChannel{Name: ch, Posts: byChannel[ch], Share: qty(some(share*100), unitPercent), Profile: profiles[source[ch]], Warn: share > channelWarnShare})
	}
	return rows, out
}

// sourceProfiles maps post sources ("telegram:<handle>", "rss:<name>") to
// the profile of the channel or feed in config.
func (w *week) sourceProfiles() map[string]string {
	out := make(map[string]string)
	for _, ch := range w.cfg.Telegram.Channels {
		out["telegram:"+ch.Handle] = ch.Profile
	}
	for _, f := range w.cfg.RSSFeeds {
		out["rss:"+f.Name] = f.Profile
	}
	return out
}

// resonanceCell is "×1.3", or a dash without a resonance.
func resonanceCell(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return "×" + num(v.V, resonanceDecimals, false)
}

// briefPosts is "Ещё мнения (кратко)": the opinions over the per-channel
// limit, one line each, loudest first.
func (w *week) briefPosts(entries []*newsEntry) []BriefPost {
	var list []*newsEntry
	for _, e := range entries {
		if e.brief {
			list = append(list, e)
		}
	}
	slices.SortStableFunc(list, byResonance)
	out := []BriefPost{}
	for _, e := range list {
		out = append(out, BriefPost{Date: Day{e.post.PublishedAt.In(w.loc)}, Author: e.post.Author, Topic: newsTopics[e.topic], FirstPhrase: firstPhrase(e), URL: e.post.URL})
	}
	return out
}

// topicGroups are the opinions or the news by topic: opinions by resonance,
// loudest first, news by time. Empty topics are left out.
func (w *week) topicGroups(entries []*newsEntry, opinions bool) []TopicGroup {
	out := []TopicGroup{}
	for topic, title := range newsTopics {
		var list []*newsEntry
		for _, e := range entries {
			if e.topic == topic && e.opinion == opinions && !e.dropped && !e.brief {
				list = append(list, e)
			}
		}
		if len(list) == 0 {
			continue
		}
		if opinions {
			slices.SortStableFunc(list, byResonance)
		}
		g := TopicGroup{Title: title, Posts: []NewsPost{}}
		for _, e := range list {
			g.Posts = append(g.Posts, w.newsPost(e.post, e.text, e.also, e.res))
		}
		out = append(out, g)
	}
	return out
}

// postViews is " — просмотры: 5.6K (×1.3)", empty without views.
func postViews(p NewsPost) string {
	if p.Views <= 0 {
		return ""
	}
	s := " — просмотры: " + compactViews(p.Views)
	if p.Resonance.Value != nil {
		s += " (" + resonanceCell(p.Resonance) + ")"
	}
	return s
}

// postTail is the line under a post: its reposts and link.
func postTail(p NewsPost) string {
	var tail []string
	if len(p.Also) > 0 {
		tail = append(tail, "Также в: "+strings.Join(p.Also, ", "))
	}
	if p.URL != "" {
		tail = append(tail, p.URL)
	}
	return strings.Join(tail, " · ")
}

// firstPhrase is an article's title, or a post's first sentence: up to the
// first line end or ". ", "! ", "? ", at most firstPhraseChars.
func firstPhrase(e *newsEntry) string {
	if e.post.Title != "" {
		return e.post.Title
	}
	s := strings.TrimSpace(e.text)
	s, _, _ = strings.Cut(s, "\n")
	for _, end := range []string{". ", "! ", "? "} {
		if i := strings.Index(s, end); i > 0 {
			s = s[:i+1]
		}
	}
	s = strings.TrimLeftFunc(s, func(r rune) bool { return isDecoration(r) || unicode.IsPunct(r) && r != '$' && r != '«' && r != '"' })
	if r := []rune(s); len(r) > firstPhraseChars {
		s = string(r[:firstPhraseChars]) + "…"
	}
	return s
}

// compactViews prints views as Telegram does: 843, 5.6K, 1.2M.
func compactViews(v int64) string {
	switch {
	case v >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(v)/1e6, 'f', 1, 64), ".0") + "M"
	case v >= 1_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(v)/1e3, 'f', 1, 64), ".0") + "K"
	}
	return strconv.FormatInt(v, 10)
}

// postText is a Telegram post in full, or an article's title and first
// articleChars characters.
func postText(p store.WeekPost) string {
	if p.Title == "" {
		return strings.TrimSpace(p.Body)
	}
	return "**" + p.Title + "**\n\n" + excerpt(p.Body, articleChars)
}

// FilteredDuplicate is a repost and its canonical post.
type FilteredDuplicate struct {
	Published time.Time `json:"published"`
	URL       string    `json:"url"`
	Main      string    `json:"main"`
}

// FilteredGroup is the posts left out for one reason.
type FilteredGroup struct {
	Reason     string              `json:"reason"`
	Count      int                 `json:"count"`
	Duplicates []FilteredDuplicate `json:"duplicates"`
	Posts      []NewsPost          `json:"posts"`
}

// NewsFilteredReport is archive/<week>/news_filtered.md: every USA post left
// out of USA News, by reason, for checking the filters. Not given to the
// agents.
type NewsFilteredReport struct {
	Header Header          `json:"header"`
	Groups []FilteredGroup `json:"groups"`
}

func (w *week) filteredReport(d *newsDoc) *NewsFilteredReport {
	byReason := make([][]newsFiltered, dropReasons)
	for _, f := range d.filtered {
		byReason[f.reason] = append(byReason[f.reason], f)
	}
	for _, e := range d.entries {
		if e.dropped {
			byReason[dropSize] = append(byReason[dropSize], newsFiltered{post: e.post, reason: dropSize, text: e.text})
		}
	}
	m := &NewsFilteredReport{Header: w.newHeader("news_filtered", "Отфильтровано из "+w.title(), nil, nil, w.from, w.to), Groups: []FilteredGroup{}}
	for r, list := range byReason {
		if len(list) == 0 {
			continue
		}
		g := FilteredGroup{Reason: dropNames[r], Count: len(list), Duplicates: []FilteredDuplicate{}, Posts: []NewsPost{}}
		for _, f := range list {
			if dropReason(r) == dropDuplicate {
				g.Duplicates = append(g.Duplicates, FilteredDuplicate{Published: f.post.PublishedAt.In(w.loc), URL: f.post.URL, Main: f.dupOf})
			} else {
				g.Posts = append(g.Posts, w.newsPost(f.post, f.text, nil, transform.Value{}))
			}
		}
		m.Groups = append(m.Groups, g)
	}
	return m
}

// UncategorizedReport is archive/<week>/uncategorized.md (§11.4): posts
// without tags, kept for review but not given to the agents.
type UncategorizedReport struct {
	Header Header     `json:"header"`
	Posts  []NewsPost `json:"posts"`
}

func (w *week) uncategorizedReport(entries []*newsEntry) *UncategorizedReport {
	m := &UncategorizedReport{Header: w.newHeader("uncategorized", "Посты без тегов — "+w.iso, nil, nil, w.from, w.to), Posts: []NewsPost{}}
	for _, e := range entries {
		m.Posts = append(m.Posts, w.newsPost(e.post, e.text, e.also, transform.Value{}))
	}
	return m
}
