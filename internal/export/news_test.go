package export

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// testPost is a post to seed: tags "tag:score,…" (first is primary),
// "junk", "" (uncategorized) or "-" (not classified yet).
type testPost struct {
	source, id, at, body, title, tags string
	opinion                           bool
	region                            string
	views                             int64
	dupOf                             string // "<source>/<id>" of the canonical post
	photo                             bool
}

func seedPosts(t *testing.T, st *store.Store, posts []testPost) {
	t.Helper()
	ids := make(map[string]int64)
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, p := range posts {
			region := p.region
			if region == "" {
				region = "usa"
			}
			handle := strings.SplitN(p.source, ":", 2)[1]
			d := model.Document{Source: p.source, ExternalID: p.id, DocType: "post", Region: region, PublishedAt: ts(p.at),
				Author: "@" + handle, Title: p.title, Body: p.body, IsOpinion: p.opinion,
				URL: "https://t.me/" + handle + "/" + p.id, Views: p.views, ViewsAt: ts(p.at).Add(time.Hour), HasPhoto: p.photo}
			if strings.HasPrefix(p.source, "rss:") {
				d.DocType, d.Author, d.URL = "article", "Example Author", "https://example.substack.com/p/"+p.id
			}
			id, _, err := store.UpsertDocument(tx, d, ts(p.at))
			if err != nil {
				return err
			}
			ids[p.source+"/"+p.id] = id
			if p.tags == "-" {
				continue
			}
			pt := store.PostTags{DocumentID: id, Classifier: "gemini", IsJunk: p.tags == "junk"}
			if p.tags != "junk" && p.tags != "" {
				for i, ts := range strings.Split(p.tags, ",") {
					tag, score, _ := strings.Cut(ts, ":")
					v, err := strconv.ParseFloat(score, 64)
					if err != nil {
						return err
					}
					pt.Tags = append(pt.Tags, store.TagScore{Tag: tag, Score: v, Primary: i == 0})
				}
			}
			if err := store.SavePostTags(tx, pt, ts(p.at)); err != nil {
				return err
			}
		}
		for _, p := range posts {
			if p.dupOf != "" {
				if err := store.SetDedup(tx, ids[p.source+"/"+p.id], "h", ids[p.dupOf]); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// weekPosts covers every case of USA News in 2026-W43
// (2026-10-18 15:00 UTC → 2026-10-25 15:00 UTC).
var weekPosts = []testPost{
	{source: "telegram:marketnews_ua", id: "100", at: "2026-10-18T13:59:00Z", body: "Before the period.", tags: "usa_monetary:0.9"},
	{source: "telegram:marketnews_ua", id: "101", at: "2026-10-19T07:30:00Z",
		body: "🏦Ймовірність утримання ставки ФРС наступного тижня вже 92% проти 84% вчора.", tags: "usa_monetary:0.95"},
	{source: "telegram:marketnews_ua", id: "102", at: "2026-10-20T12:05:00Z",
		body: "🇺🇸 Мінфін збільшить розміщення bills, TGA відновлюється.\nДохідності 2Y +6 б.п.", tags: "usa_fiscal:0.9,usa_monetary:0.6"},
	{source: "telegram:macro_views", id: "200", at: "2026-10-21T09:00:00Z", opinion: true,
		body: "Fed переписал reaction function.\n\nРынок получил не одиночный insurance hike, а подтверждение нового режима.", tags: "usa_monetary:0.9"},
	{source: "telegram:marketnews_ua", id: "103", at: "2026-10-21T10:00:00Z", body: "Fed переписал reaction function (репост).",
		tags: "usa_monetary:0.9", dupOf: "telegram:macro_views/200"},
	{source: "telegram:macro_views", id: "201", at: "2026-10-21T11:00:00Z", body: "Repost of my own post.",
		tags: "usa_monetary:0.9", opinion: true, dupOf: "telegram:macro_views/200"},
	{source: "telegram:marketnews_ua", id: "104", at: "2026-10-22T13:49:00Z", body: "🔽📖$META -7%", tags: "usa_micro:1.0"},
	{source: "telegram:marketnews_ua", id: "105", at: "2026-10-23T08:00:00Z",
		body: "🇫🇷 Moody’s зберегло рейтинг Франції на рівні «Aa3».", tags: "eu_fiscal:0.95,eu_macro:0.7"},
	{source: "telegram:marketnews_ua", id: "106", at: "2026-10-23T09:00:00Z", body: "🎁 РОЗІГРАШ! Підпишись на канал партнера", tags: "junk"},
	{source: "telegram:marketnews_ua", id: "107", at: "2026-10-23T10:00:00Z", body: "😒Трамп: Ми можемо забрати усю іранську нафту собі!", tags: ""},
	{source: "telegram:marketnews_ua", id: "108", at: "2026-10-25T14:50:00Z", body: "Ще не розмічений пост.", tags: "-"},
	{source: "telegram:marketnews_ua", id: "109", at: "2026-10-24T10:00:00Z", body: "Repost of a post from last week.",
		tags: "usa_monetary:0.9", dupOf: "telegram:marketnews_ua/100"},
	// FIXES_W44 batch E: facts with expectations, a calendar announcement,
	// US politics, energy with and without a US link, an opinion marker in a
	// news channel with a promo line.
	{source: "telegram:marketnews_ua", id: "110", at: "2026-10-22T12:31:00Z",
		body: "🇺🇸Показники:\n\n▫️PCE Price Index MoM 0.5%✅\n\n▫️GDP Q3 (1st) 3.4% (очік. 3.2%🔼)\n\n▫️IJC 194 тис. (очік. 198 тис.🔽)", tags: "usa_monetary:0.95"},
	{source: "telegram:marketnews_ua", id: "111", at: "2026-10-22T06:00:00Z",
		body: "Основні події на ринках сьогодні:\n\n🔎 На що звернути увагу:\n🇺🇸 CB Consumer Confidence - 16:00\n- попередній показник: 81.9", tags: "usa_monetary:0.7"},
	{source: "telegram:marketnews_ua", id: "112", at: "2026-10-23T11:00:00Z", views: 5612,
		body: "🇺🇸 Трамп: мита на імпорт з Мексики зростуть до 25% з 1 грудня. Futures на S&P 500 -0.8%.", tags: "usa_politics:0.95"},
	{source: "telegram:marketnews_ua", id: "113", at: "2026-10-23T12:00:00Z",
		body: "🛢 США запровадили санкції проти Rosneft і Lukoil, WTI +5%. Бензин в Штатах подорожчає.", tags: "energy_geopolitics:0.9,usa_politics:0.6"},
	{source: "telegram:marketnews_ua", id: "114", at: "2026-10-23T13:00:00Z",
		body: "🛢 Brent +4% до $86: Іран погрожує перекрити Ормузьку протоку після удару Ізраїлю.", tags: "energy_geopolitics:0.95"},
	{source: "telegram:marketnews_ua", id: "115", at: "2026-10-24T09:00:00Z",
		body: "Я думаю, ФРС знизить ставку в грудні: ринок праці слабшає швидше, ніж очікувалось.\n⚡️ Розбір — у нашій спільноті Market News Bureau!", tags: "usa_monetary:0.8"},
	{source: "telegram:euro_rates", id: "1", at: "2026-10-22T08:00:00Z", body: "ЄЦБ: ставка без змін.", tags: "eu_ecb:0.9", region: "eu"},
	{source: "rss:macro_letter", id: "warsh", at: "2026-10-24T07:14:00Z", opinion: true, title: "The Maradona Theory of Interest Rates",
		body: strings.Repeat("Warsh says something hawkish now, something dovish later, and Fed Funds stay where they are. ", 30),
		tags: "usa_monetary:0.9,usa_fiscal:0.6"},
}

// The file over the size limit (§12.6): news posts with the fewest views go
// whole; opinions stay and no text is ever cut.
func TestNewsSizeLimit(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	long := strings.Repeat("Довгий текст про ставку ФРС і ринок облігацій. ", 60) // ~2 900 chars
	seedPosts(t, st, []testPost{
		{source: "telegram:macro_views", id: "1", at: "2026-10-20T09:00:00Z", opinion: true, body: "Low views opinion. " + long, tags: "usa_monetary:0.6", views: 10},
		{source: "telegram:macro_views", id: "2", at: "2026-10-20T10:00:00Z", opinion: true, body: "High views opinion. " + long, tags: "usa_monetary:0.95", views: 9000},
		{source: "telegram:marketnews_ua", id: "3", at: "2026-10-20T11:00:00Z", body: "News many views. " + long, tags: "usa_fiscal:0.9", views: 8000},
		{source: "telegram:marketnews_ua", id: "4", at: "2026-10-20T12:00:00Z", body: "News few views. " + long, tags: "usa_fiscal:0.9", views: 500},
		{source: "telegram:marketnews_ua", id: "5", at: "2026-10-20T13:00:00Z", body: "News unknown views. " + long, tags: "usa_politics:0.9"},
	})
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	render := func(limit int) (string, string) {
		cfg.Export.MaxFileChars = limit
		w, err := x.newWeek(context.Background(), end)
		if err != nil {
			t.Fatal(err)
		}
		w.useReport(config.ReportNews)
		news, filtered, _, err := w.news(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return news, filtered
	}
	full, _ := render(0)
	if n := len([]rune(full)); n < 14000 || !strings.Contains(full, "size_dropped: 0, uncategorized: 0, opinions: 2, opinions_brief: 0, news: 3 }") {
		t.Fatalf("full file %d chars", n)
	}

	// Room for four posts: the news post without views goes first.
	news, filtered := render(len([]rune(full)) - 2000)
	if strings.Contains(news, "News unknown views") || !strings.Contains(news, "News few views") ||
		!strings.Contains(news, "size_dropped: 1,") || !strings.Contains(news, "| Лимит размера файла | 1 |") ||
		!strings.Contains(filtered, "## Лимит размера файла — 1\n\n#### 2026-10-20 15:00 — @marketnews_ua\nNews unknown views. ") {
		t.Errorf("drop step:\n%s\n%s", news[:900], filtered)
	}

	// Smaller than the opinions: every news post goes, the opinions stay
	// whole, the file stays over the limit.
	news, _ = render(3000)
	if _, posts, _ := strings.Cut(news, "## 2."); strings.Contains(posts, "News many views") || !strings.Contains(news, "Low views opinion. "+strings.TrimSpace(long)) ||
		!strings.Contains(news, "High views opinion") || !strings.Contains(news, "size_dropped: 3,") || strings.Contains(news, "[обрезано") {
		t.Errorf("opinions only (%d chars):\n%s", len([]rune(news)), news)
	}
	// Opinions by views: the most viewed first.
	if i, j := strings.Index(news, "High views opinion"), strings.Index(news, "Low views opinion"); i < 0 || j < i {
		t.Errorf("opinion order: high at %d, low at %d", i, j)
	}
}

// E1, E3, E4 of FIXES_W44 and the noise filters of 2026-11-03: signatures,
// promo lines, image-only posts, captions, announcements, product news,
// opinion markers, topics and the size of "Глобально".
func TestNewsCleanup(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.News.GlobalMax = 2
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sig := "\n\nLearn ◽️ Explore ◽️ Channel"
	seedPosts(t, st, []testPost{
		// Four posts of an opinion channel, three with its signature.
		{source: "telegram:macro_views", id: "1", at: "2026-10-20T09:00:00Z", opinion: true, body: "Кривая UST снова круче: 2s10s вышел в плюс на 40bp." + sig, tags: "usa_fiscal:0.9"},
		{source: "telegram:macro_views", id: "2", at: "2026-10-20T10:00:00Z", opinion: true, body: "Term premium на максимуме с 2014 года, рынок требует больше за риск." + sig, tags: "usa_fiscal:0.8"},
		{source: "telegram:macro_views", id: "3", at: "2026-10-20T11:00:00Z", opinion: true, body: "Nvidia снова тянет весь индекс, breadth рынка слабый." + sig, tags: "usa_micro:0.8"},
		{source: "telegram:macro_views", id: "4", at: "2026-10-20T12:00:00Z", opinion: true, body: "Без подписи: рынок закладывает две ставки снижения до марта.", tags: "usa_monetary:0.8"},
		// News channel: image with a caption, promo, opinion markers.
		{source: "telegram:marketnews_ua", id: "10", at: "2026-10-21T09:00:00Z", body: "🔽BTC = $78900", tags: "usa_micro:0.6"},
		{source: "telegram:marketnews_ua", id: "11", at: "2026-10-21T10:00:00Z", body: "🎙️ Трансляція для підписників спільноти о 19:45", tags: "usa_monetary:0.6"},
		{source: "telegram:marketnews_ua", id: "12", at: "2026-10-21T11:00:00Z", body: "IMO the Fed is behind the curve: core PCE runs at 3.1% and rising.", tags: "usa_monetary:0.9"},
		{source: "telegram:marketnews_ua", id: "13", at: "2026-10-21T12:00:00Z", body: "Для меня главный риск недели — аукцион 30-летних бумаг Минфина.", tags: "usa_fiscal:0.9"},
		{source: "telegram:marketnews_ua", id: "14", at: "2026-10-21T13:00:00Z", body: "Мінфін США розмістить bills на $100 млрд, TGA відновлюється.\n\n👉 Market News Bureau", tags: "usa_fiscal:0.9"},
		// "Глобально": three energy posts without a US link, room for two.
		{source: "telegram:marketnews_ua", id: "20", at: "2026-10-22T09:00:00Z", body: "ОПЕК+ зберегла скорочення видобутку нафти до кінця першого кварталу.", tags: "energy_geopolitics:0.7"},
		{source: "telegram:marketnews_ua", id: "21", at: "2026-10-22T10:00:00Z", body: "Brent +4% до $86: Іран погрожує перекрити Ормузьку протоку.", tags: "energy_geopolitics:0.95"},
		{source: "telegram:marketnews_ua", id: "22", at: "2026-10-22T11:00:00Z", body: "Росія скоротила експорт дизелю через атаки на НПЗ, ціни ростуть.", tags: "energy_geopolitics:0.9"},
		// Noise filters (W44 posts): a countdown, a chart caption, a product
		// launch; a short quote with a photo (no caption word) and a product
		// launch with a price move stay.
		{source: "telegram:marketnews_ua", id: "30", at: "2026-10-23T09:00:00Z", body: "❗️🏦Публікація рішення FOMC - вже за 10 хвилин!", tags: "usa_monetary:0.9"},
		{source: "telegram:marketnews_ua", id: "31", at: "2026-10-23T10:00:00Z", body: "⚡️Хроніка FOMC та ринкової реакції після засідань.", tags: "usa_monetary:0.6", photo: true},
		{source: "telegram:marketnews_ua", id: "35", at: "2026-10-23T10:10:00Z", photo: true, tags: "usa_micro:0.9",
			body: "👀У додаток до теми!\n\nДинаміка зростання корпоративних прибутків.\n\nЛіворуч - після звітностей, праворуч - до початку сезону звітностей станом на кінець Q3."},
		{source: "telegram:marketnews_ua", id: "36", at: "2026-10-23T10:20:00Z", photo: true, tags: "usa_monetary:0.95",
			body: "🏦Агреговані ймовірності після FOMC:\n\n▫️скорочення ймовірності двох поспіль підвищень ставки у грудні та січні з 37% до 20%."},
		{source: "telegram:marketnews_ua", id: "32", at: "2026-10-23T11:00:00Z", body: "🍏Apple релізить MacBook Pro 14” на М6 та іМас на М6.", tags: "usa_micro:0.7"},
		{source: "telegram:marketnews_ua", id: "33", at: "2026-10-23T12:00:00Z", body: "😒Трамп: Я не доручав Венсу писати іранцям.", tags: "usa_politics:0.9", photo: true},
		{source: "telegram:marketnews_ua", id: "34", at: "2026-10-23T13:00:00Z", body: "🍏Apple представила iPhone Fold, $AAPL +3% на пре-маркеті.", tags: "usa_micro:0.7"},
		// Topics of usa_micro and untagged posts.
		{source: "telegram:marketnews_ua", id: "40", at: "2026-10-24T09:00:00Z", body: "CTA продовжують купувати: позиціонування фондів найвище з лютого.", tags: ""},
		{source: "telegram:marketnews_ua", id: "41", at: "2026-10-24T10:00:00Z", body: "📑Після звітностей: $MSFT +5%, $GOOG -7.5%, виручка обох вище прогнозів.", tags: "usa_micro:0.9"},
	})
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	w, err := x.newWeek(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	w.useReport(config.ReportNews)
	news, filtered, _, err := w.news(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	section := func(title string) string {
		_, after, _ := strings.Cut(news, "## "+title+"\n")
		body, _, _ := strings.Cut(after, "\n## ")
		return body
	}
	topic := func(part, title string) string {
		_, after, _ := strings.Cut(section(part), "### "+title+"\n")
		body, _, _ := strings.Cut(after, "\n### ")
		return body
	}
	opinions, newsPart := "2. Мнения и аналитика", "3. Новости"
	checks := []struct {
		name string
		ok   bool
	}{
		{"signature cut", !strings.Contains(news, "Learn ◽️ Explore")},
		{"signature post kept", strings.Contains(news, "2s10s вышел в плюс")},
		{"image-only dropped", !strings.Contains(news, "BTC = $78900")},
		{"promo-only post dropped", !strings.Contains(news, "Трансляція")},
		{"Market News Bureau link line cut", !strings.Contains(news, "Market News Bureau") && strings.Contains(news, "TGA відновлюється.\n")},
		{"stats", strings.Contains(news, "image_only_dropped: 2, calendar_dropped: 0, announce_dropped: 1, caption_dropped: 2, product_dropped: 1, eu_only: 0, global_dropped: 1, size_dropped: 0, uncategorized: 0, opinions: 6, opinions_brief: 0, news: 8 }")},
		{"IMO is an opinion", strings.Contains(topic(opinions, "ФРС и ставки"), "IMO the Fed")},
		{"для меня is an opinion", strings.Contains(topic(opinions, "Фискал и госдолг"), "Для меня")},
		{"no marker is news", strings.Contains(topic(newsPart, "Фискал и госдолг"), "Мінфін США")},
		{"opinion channel stays opinion", strings.Contains(topic(opinions, "ФРС и ставки"), "Без подписи")},
		{"global keeps top score", strings.Contains(topic(newsPart, "Глобально"), "Ормузьку") && strings.Contains(topic(newsPart, "Глобально"), "дизелю")},
		{"global drops low score", !strings.Contains(news, "ОПЕК+")},
		{"countdown dropped", !strings.Contains(news, "вже за 10 хвилин")},
		{"caption dropped", !strings.Contains(news, "Хроніка FOMC")},
		{"chart caption with digits dropped", !strings.Contains(news, "У додаток до теми")},
		{"photo post with numbers kept", strings.Contains(topic(newsPart, "ФРС и ставки"), "Агреговані ймовірності")},
		{"product dropped", !strings.Contains(news, "MacBook Pro")},
		{"short photo post without a caption word kept", strings.Contains(topic(newsPart, "Политика США"), "Я не доручав")},
		{"product with a move kept", strings.Contains(topic(newsPart, "Корпоративные отчёты"), "iPhone Fold")},
		{"usa_micro with AI words", strings.Contains(topic(opinions, "AI и техсектор"), "Nvidia снова")},
		{"untagged market post", strings.Contains(topic(newsPart, "Рынок и позиционирование"), "CTA продовжують")},
		{"usa_micro earnings", strings.Contains(topic(newsPart, "Корпоративные отчёты"), "Після звітностей")},
		{"no empty topics", !strings.Contains(news, "Нет постов за неделю")},
		{"filtered reasons", strings.Contains(news, "| Анонсы, отсчёты, «скоро выйдет» | 1 |") && strings.Contains(news, "| Подписи к картинкам | 2 |")},
		{"filtered archive", strings.Contains(filtered, "## Продуктовые новости — 1\n\n#### 2026-10-23 13:00 — @marketnews_ua\n🍏Apple релізить") &&
			strings.Contains(filtered, "## «Глобально» сверх лимита — 1")},
		{"channel warning", strings.Contains(news, "⚠ @marketnews_ua — 71% постов недели")},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s:\n%s", c.name, news)
			break
		}
	}
}

func TestCompactViews(t *testing.T) {
	for v, want := range map[int64]string{843: "843", 1000: "1K", 5612: "5.6K", 12049: "12K", 1_260_000: "1.3M"} {
		if got := compactViews(v); got != want {
			t.Errorf("compactViews(%d) = %q, want %q", v, got, want)
		}
	}
}

// Review of W44 (2026-11-03): posts compare by resonance (views over the
// channel's median), a channel shows opinions_per_channel opinions in full
// (its own opinions_full if set) and the rest as one line each, the
// overview names each channel's profile.
func TestNewsResonance(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.News.OpinionsPerChannel = 6
	cfg.Telegram.Channels = append(cfg.Telegram.Channels, config.Channel{Handle: "bigpolitics", Region: "usa", Type: "opinion", Profile: "политический комментарий, факты не проверены", OpinionsFull: 5})
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var posts []testPost
	for i, v := range []int64{15000, 15500, 16000, 14500, 39500, 15200, 15800} {
		posts = append(posts, testPost{source: "telegram:bigpolitics", id: strconv.Itoa(i + 1), at: fmt.Sprintf("2026-10-%02dT09:00:00Z", 19+i%6),
			opinion: true, views: v, tags: "usa_politics:0.95",
			body: fmt.Sprintf("Big opinion %d. Выборы в Конгресс и Трамп: длинный оценочный текст.", i+1)})
	}
	// An earlier week of the small channel: its usual is about 900.
	for i, v := range []int64{850, 900, 880, 920} {
		posts = append(posts, testPost{source: "telegram:macro_views", id: strconv.Itoa(100 + i), at: fmt.Sprintf("2026-10-%02dT09:00:00Z", 6+i),
			opinion: true, views: v, tags: "usa_monetary:0.9", body: "Old opinion about the Fed and rates."})
	}
	posts = append(posts, testPost{source: "telegram:macro_views", id: "200", at: "2026-10-21T09:00:00Z", opinion: true, views: 1200,
		tags: "usa_monetary:0.9", body: "Small loud opinion. ФРС повысит ставку в декабре."})
	seedPosts(t, st, posts)
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	w, err := x.newWeek(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	w.useReport(config.ReportNews)
	news, _, _, err := w.news(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, top, _ := strings.Cut(news, "### Топ-5 постов по резонансу\n")
	top, _, _ = strings.Cut(top, "\n### ")
	checks := []struct {
		name string
		ok   bool
	}{
		{"small channel's loud post first", strings.Contains(top, "| ×1.3 | 1.2K | @macro_views | Small loud opinion. |")},
		{"two posts per channel", strings.Count(top, "@bigpolitics") == 2},
		{"big outlier second", strings.Contains(top, "| ×2.5 | 39.5K | @bigpolitics | Big opinion 5. |")},
		{"five of the channel in full", strings.Count(news, "#### ") == 6 && strings.Contains(news, "Big opinion 5. Выборы")},
		{"the quietest two as one line", strings.Contains(news, "### Ещё мнения (кратко)\n") &&
			strings.Contains(news, "- 2026-10-22 — @bigpolitics — Политика США — Big opinion 4. — https://t.me/bigpolitics/4\n") &&
			strings.Contains(news, "- 2026-10-19 — @bigpolitics — Политика США — Big opinion 1. — https://t.me/bigpolitics/1\n")},
		{"stats", strings.Contains(news, "opinions: 6, opinions_brief: 2, news: 0 }")},
		{"resonance in the header", strings.Contains(news, "— просмотры: 1.2K (×1.3)\nSmall loud opinion.")},
		{"profile", strings.Contains(news, "| @bigpolitics | 7 | 88% | политический комментарий, факты не проверены |")},
		{"no profile is a dash", strings.Contains(news, "| @macro_views | 1 | 13% | рус. макро-аналитика")},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s:\n%s", c.name, news)
			break
		}
	}
}
