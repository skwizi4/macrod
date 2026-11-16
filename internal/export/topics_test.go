package export

import (
	"strconv"
	"strings"
	"testing"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

func TestTopicCounts(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	topics := cfg.News.MentionTopics
	idx := map[string]int{}
	for i, tp := range topics {
		idx[tp.Name] = i
	}
	post := func(id int64, body string) store.WeekPost {
		return store.WeekPost{Document: model.Document{ID: id, Source: "telegram:marketnews_ua", Author: "@marketnews_ua", Region: "usa", Body: body},
			Classified: true}
	}
	posts := []store.WeekPost{
		post(1, "Ймовірність рецесії в США зросла до 35%, кажуть економісти Goldman."),                                  // рецессия (uk)
		post(2, "Рынок закладывает снижение ставки ФРС в декабре — два снижения до лета."),                              // снижение (ru)
		post(3, "Traders now price a Fed rate hike in March as inflation re-accelerates."),                              // повышение (en)
		post(4, "Microsoft підняла capex до $45 млрд, ринок боїться бульбашки ШІ."),                                     // AI
		post(5, "Brent +4% after OPEC+ cut; нафта дорожчає третій день поспіль."),                                       // нефть (одна на пост)
		post(6, "Трамп: мита на імпорт з Мексики зростуть до 25%. Tariffs on EU next."),                                 // тарифы
		post(7, "Shutdown day 12: Senate vote fails again; шатдаун триває."),                                            // шатдаун
		post(8, "Private credit funds see redemptions; spreads on high yield widen 40 bp."),                             // кредитный стресс
		post(9, "Midterm elections on Tuesday: markets watch the House race."),                                          // выборы
		post(10, "Ставки по ипотеке в США выросли до 6.8%."),                                                            // ничего: «ставки» без снижения/повышения
		{Document: model.Document{ID: 11, Region: "usa", Body: "Ймовірність рецесії 40%."}, Classified: true, DupOf: 1}, // репост
		{Document: model.Document{ID: 12, Region: "usa", Body: "Recession odds jump on Polymarket after the payrolls miss."}, Classified: true},
		{Document: model.Document{ID: 13, Region: "usa", Body: "Recession odds jump again."}, Classified: false},       // не размечен
		{Document: model.Document{ID: 14, Region: "eu", Body: "Recession in Germany."}, Classified: true},              // EU
		{Document: model.Document{ID: 15, Region: "usa", Body: "Recession giveaway!", IsJunk: true}, Classified: true}, // мусор
		post(16, "Основні події на ринках сьогодні:\n🇺🇸 Рішення ФРС щодо ставки, ризик рецесії"),                       // анонс календаря
		post(17, "hard landing"), // < 40 символов — картинка с подписью
		post(18, "Ціни на нафти марки WTI злетіли після заяви Ірану про протоку."),         // нефть: основа «нафт»
		post(19, "На виборах до Конгресу демократи лідирують у всіх ключових гонках."),     // выборы: основа «вибор»
		post(20, "Перед выборами в США рынок нервничает: опросы противоречат друг другу."), // выборы: основа «выбор»
	}
	news := newsReportOf(cfg)
	inNews := func(p store.WeekPost) bool { return p.Region == "usa" }
	got := topicCounts(cfg.News, news, inNews, cfg.Classify.Rules, posts)
	want := map[string]int{"Рецессия": 2, "Снижение ставки": 1, "Повышение ставки": 1, "AI bubble / AI capex": 1, "Нефть": 2,
		"Тарифы": 1, "Шатдаун": 1, "Кредитный стресс": 1, "Выборы": 3}
	for name, n := range want {
		if got[idx[name]] != n {
			t.Errorf("%s = %d, want %d", name, got[idx[name]], n)
		}
	}
}

// Topics of W44 posts the review found misplaced or untagged (2026-11-03):
// the words decide, the classifier's tag is a hint.
func TestNewsTopicVote(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cl := newNewsCleaner(cfg.News, newsReportOf(cfg), cfg.Classify.Rules, nil)
	tags := func(s string) []store.TagScore {
		var out []store.TagScore
		for _, ts := range strings.Split(s, ",") {
			if ts == "" {
				continue
			}
			tag, score, _ := strings.Cut(ts, ":")
			v, _ := strconv.ParseFloat(score, 64)
			out = append(out, store.TagScore{Tag: tag, Score: v})
		}
		return out
	}
	for _, tt := range []struct {
		name, tags, body string
		want             int
	}{
		{"Brazil → US Congress, weak fiscal tag", "usa_fiscal:0.5",
			"🇧🇷Мабуть вже бачили, що через неочікувані результати виборів бразильський ринок обвалився на усе, що набрав за 2026, втративши -30%, а валюта -10% інтрадей?\n\nСьогоднішній дроп $EWZ стає лише репетицією глобальної волатильності від виборів у Конгрес США, котра на нас очікує за відхилень від естімейтів.", topicPolitics},
		{"Commonwealth, eu primary and weak usa", "eu_fiscal:0.9,usa_politics:0.5",
			"Платить и каяться. Ежегодный саммит государств британского “содружества наций” вновь проходит под лозунгом выплаты репараций.", topicGlobal},
		{"Kalshi round", "usa_micro:0.8", "⚡️Kalshi проведе раунд на чолі з Sequoia та Wellington із залученням $1 млрд, оцінка = $40 млрд.", topicMarket},
		{"Chinese memory deals", "usa_micro:0.9",
			"🇨🇳Alibaba, Tencent та ByteDance (TikTok) прагнуть забезпечити себе дефіцитною памʼяттю через наймасштабніші 10-річні угоди з корейськими гігантами Samsung та SKHynix.", topicAI},
		{"remember is not memory", "usa_micro:0.9", "👀Пам’ятаєте відскок $META посеред Q3 на найнижчих за 10+ років мультиплікаторах оцінки?", topicEarnings},
		{"untagged Trump on Iran", "", "😒Трамп:\nЯ не доручав Венсу писати іранцям.", topicPolitics},
		{"untagged bitcoin", "", "💸BTC опустився нижче $80 тисяч і може закрити день нижче цієї позначки вперше за 5 тижнів.", topicMarket},
		{"untagged NBU", "", "🏦🇺🇦 НБУ підвищив облікову ставку до 16.5%\n\n🔎 Інфляція у вересні пришвидшилася до 10%.", topicGlobal},
		{"untagged BoJ", "", "🇯🇵Уеда (BoJ) після релізу свіжих макроданих Японії натякнув на агресивніший монетарний курс.", topicGlobal},
		{"energy without a US link", "energy_geopolitics:0.95", "🛢 Brent +4% до $86: Іран погрожує перекрити Ормузьку протоку після удару Ізраїлю.", topicGlobal},
		{"energy with a US link", "energy_geopolitics:0.9,usa_politics:0.6", "🛢 США запровадили санкції проти Rosneft і Lukoil, WTI +5%.", topicEnergy},
		{"a strong tag stays", "usa_monetary:0.95", "🏦Ймовірність утримання ставки ФРС наступного тижня вже 92% проти 84% вчора.", topicFed},
		{"only eu tags: shared file", "eu_ecb:0.9", "ЄЦБ: ставка без змін.", topicEurope},
		{"energy with an eu link", "energy_geopolitics:0.9,eu_macro:0.6", "Росія скоротила експорт дизелю через атаки на НПЗ, ціни ростуть.", topicEnergy},
		{"gas price: eu words", "energy_geopolitics:0.9,eu_macro:0.6", "🛢 TTF +8%: Норвегія зупинила експорт газу до Німеччини.", topicEurope},
		{"no tags, no words", "", "Розберемо 🤝", topicNone},
	} {
		p := store.WeekPost{Document: model.Document{Region: "usa", Body: tt.body}, Classified: true, Tags: tags(tt.tags)}
		if got := cl.topic(p, tt.body); got != tt.want {
			t.Errorf("%s: topic %d, want %d", tt.name, got, tt.want)
		}
	}

	// A news file of one region (no shared): another region's posts are its news.
	one := *newsReportOf(cfg)
	one.Shared, one.OwnTagPrefixes, one.OwnTagPrefix, one.OtherTagPrefix = false, nil, "usa_", "eu_"
	one.TagTopics = map[string]string{"usa_monetary": config.TopicFed, "usa_fiscal": config.TopicFiscal, "usa_politics": config.TopicPolitics,
		"energy_geopolitics": config.TopicEnergy, "usa_micro": config.TopicEarnings}
	cl = newNewsCleaner(cfg.News, &one, cfg.Classify.Rules, nil)
	for _, tt := range []struct {
		name, tags, body string
		want             int
	}{
		{"only eu tags", "eu_ecb:0.9", "ЄЦБ: ставка без змін.", topicOtherRegion},
		{"Commonwealth, eu primary and weak usa", "eu_fiscal:0.9,usa_politics:0.5", "Саммит содружества наций.", topicGlobal},
		{"energy with an eu link only", "energy_geopolitics:0.9,eu_macro:0.6", "Росія скоротила експорт дизелю через атаки на НПЗ, ціни ростуть.", topicGlobal},
	} {
		p := store.WeekPost{Document: model.Document{Region: "usa", Body: tt.body}, Classified: true, Tags: tags(tt.tags)}
		if got := cl.topic(p, tt.body); got != tt.want {
			t.Errorf("one region, %s: topic %d, want %d", tt.name, got, tt.want)
		}
	}
}
