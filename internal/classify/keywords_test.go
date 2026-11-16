package classify

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"macro-collector/internal/config"
)

func testKeywords(t *testing.T) *Keywords {
	t.Helper()
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return NewKeywords(cfg.Classify)
}

func TestWords(t *testing.T) {
	got := words("🔥 $META -7%! Звʼязок, Ёлка https://t.me/x/1 EUR/USD S&P500 don't")
	want := []string{"meta", "7", "звʼязок", "елка", "eur", "usd", "s", "p500", "don't"}
	if !slices.Equal(got, want) {
		t.Errorf("words = %q, want %q", got, want)
	}
}

func TestKeywordsClassify(t *testing.T) {
	k := testKeywords(t)
	tests := []struct {
		name, text, defaultTag string
		tags                   []string // in primary-first order; nil = uncategorized
		junk                   bool
	}{
		{"prefix and phrase", "🏦Ймовірність утримання ставки ФРС наступного тижня вже 92%", "", []string{"usa_monetary"}, false},
		{"one hit is not enough", "Інфляція прискорилась.", "", nil, false},
		{"default tag adds a point", "Інфляція прискорилась.", "usa_monetary", []string{"usa_monetary"}, false},
		{"russian, tie by tag order", "Пауэлл намекнул на снижение ставки, доходности трежерис упали", "", []string{"usa_monetary", "usa_fiscal"}, false},
		{"two tags", "Мінфін США збільшить аукціони, дефіцит бюджету росте; ФРС і інфляція — далі", "", []string{"usa_fiscal", "usa_monetary"}, false},
		{"english", "Nvidia earnings beat, guidance raised on AI capex", "", []string{"usa_micro"}, false},
		{"europe", "ЄЦБ залишив депозитну ставку, Лагард: інфляція в єврозоні під контролем", "", []string{"eu_ecb"}, false},
		{"junk ukrainian", "🎁 РОЗІГРАШ! Підпишись на канал партнера", "", nil, true},
		{"junk prefix", "Знижки до 50% за промокодом", "", nil, true},
		{"crypto only", "#Crypto 📉 $BTC нижче $80 000, ліквідовано 228 тис. трейдерів", "", nil, false},
		{"links ignored", "https://t.me/fed_cpi_inflation/1 читайте", "", nil, false},
		{"us politics ukrainian", "🇺🇸 Трамп: мита на імпорт з Мексики зростуть до 25%", "", []string{"usa_politics"}, false},
		{"us politics english", "Congress heads for a shutdown as the White House rejects the deal", "", []string{"usa_politics"}, false},
		{"us politics russian", "Выборы в Конгресс: рынки закладывают победу демократов", "", []string{"usa_politics"}, false},
		{"energy ukrainian", "🛢 Brent +4%: Іран погрожує перекрити протоку після удару Ізраїлю", "", []string{"energy_geopolitics"}, false},
		{"energy russian", "Нефть дорожает: ОПЕК+ сохранила сокращение добычи", "", []string{"energy_geopolitics"}, false},
		{"energy and politics", "Trump and Congress agree new sanctions on Russia oil exports, WTI jumps", "", []string{"energy_geopolitics", "usa_politics"}, false},
		{"stem does not overmatch", "Миттєва реакція ринку: митниця Польщі", "", nil, false},
		// Stems (FIXES_W44 G3): inflected forms match.
		{"stems ukrainian", "Ціни на нафту ростуть через нові санкціями проти Росії", "", []string{"energy_geopolitics"}, false},
		{"stems russian", "Цены нефти выросли: санкциях против Ирана", "", []string{"energy_geopolitics"}, false},
		{"stems politics", "На виборах до Конгресу демократи йдуть попереду, Трампа критикують", "", []string{"usa_politics"}, false},
		{"stems politics russian", "Перед выборами в Конгрессе республиканцы и Белого дома спорят", "", []string{"usa_politics"}, false},
		{"stems english", "Congressional elections: sanctioned Iranian oil", "", []string{"energy_geopolitics", "usa_politics"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := k.Classify(context.Background(), []Post{{Text: tt.text, DefaultTag: tt.defaultTag}})
			r := res[0]
			var tags []string
			for _, tg := range r.Tags {
				tags = append(tags, tg.Tag)
			}
			if !slices.Equal(tags, tt.tags) || r.IsJunk != tt.junk || r.By != ByKeywords {
				t.Errorf("tags = %v junk = %v by = %s, want %v junk = %v", tags, r.IsJunk, r.By, tt.tags, tt.junk)
			}
			if want := ""; len(tt.tags) > 0 {
				want = tt.tags[0]
				if r.Primary != want {
					t.Errorf("primary = %q, want %q", r.Primary, want)
				}
			} else if r.Primary != want {
				t.Errorf("primary = %q, want empty", r.Primary)
			}
		})
	}
}

// The keyword fallback on the same labeled examples as Gemini: much weaker,
// it only has to keep junk out and get the clear cases.
func TestKeywordsLabeledQuality(t *testing.T) {
	ls, err := LoadLabeled(filepath.Join("..", "..", "testdata", "classify", LabeledFile))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := testKeywords(t).Classify(context.Background(), LabeledPosts(ls))
	ok := agreement(t, ls, res)
	t.Logf("keywords: %d of %d labeled examples agree", ok, len(ls))
	if ok*100 < len(ls)*KeywordsMinAgreement {
		t.Errorf("keywords agreement %d/%d < %d%%", ok, len(ls), KeywordsMinAgreement)
	}
}

// KeywordsMinAgreement is the floor of the keyword fallback, set from its
// result on the labeled examples.
const KeywordsMinAgreement = 60
