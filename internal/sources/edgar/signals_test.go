package edgar

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/irtranscript"
)

// rules are the macro signal rules of the committed config.yaml.
func rules(t *testing.T) SignalRules {
	t.Helper()
	cfg, err := config.LoadAt(filepath.Join("..", "..", "..", "presets", "us-equities.yaml"), filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.EDGAR.MacroSignals
	r := SignalRules{Exclude: m.Excluded, Strong: m.Strongs, MinChars: m.MinChars, MaxChars: m.MaxChars,
		PerTopic: m.PerCompanyTopic, PerCompany: m.PerCompany}
	for _, tp := range m.Topics {
		r.Topics = append(r.Topics, Topic{Name: tp.Name, Match: tp.Match})
	}
	return r
}

func TestSentences(t *testing.T) {
	got := Sentences("• Net sales grew 12% to $0.9 billion. Excluding U.S. sales, growth was 8%.\n\n“We see demand,” said Jane. 2027 will be big! Really? yes.")
	want := []string{"Net sales grew 12% to $0.9 billion.", "Excluding U.S. sales, growth was 8%.", "“We see demand,” said Jane.", "2027 will be big!", "Really? yes."}
	if !slices.Equal(got, want) {
		t.Errorf("sentences = %q", got)
	}
}

func pick(t *testing.T, add func(p *SignalPicker)) []Signal {
	t.Helper()
	p := NewSignalPicker(rules(t))
	add(p)
	return p.Signals()
}

// MSFT FY27 Q1 call (recorded 2026-10-30), rules of 2026-10-31: 6 signals,
// all with strong phrases (the review's examples); the rambling Q&A answer
// on memory ("the example Satya gave for Adam's question") is out; no FX;
// at most 2 per topic. Topic priority (FIXES_W44 D2): memory is last, so a
// second demand sentence wins the sixth place; memory stays in the capacity
// sentence.
func TestSignalsMicrosoftCall(t *testing.T) {
	text, err := irtranscript.Text(read(t, filepath.Join("..", "ir", "msft_fy-2027_q1.html")))
	if err != nil {
		t.Fatal(err)
	}
	qa := irtranscript.QAStart(text)
	if qa >= len(text) || !strings.Contains(text[qa:qa+200], "Q&A") {
		t.Fatalf("Q&A start at %d: %q", qa, text[qa:min(len(text), qa+200)])
	}
	sigs := pick(t, func(p *SignalPicker) {
		p.Add(text[:qa], "звонок", false)
		p.Add(text[qa:], "звонок, Q&A", true)
	})
	if len(sigs) != 6 {
		t.Errorf("%d signals, want 6:\n%v", len(sigs), sigs)
	}
	has := func(topic, part string) bool {
		return slices.ContainsFunc(sigs, func(s Signal) bool { return s.Topic == topic && strings.Contains(s.Sentence, part) })
	}
	for _, w := range []struct{ topic, part string }{
		{"спрос", "Customer demand continues to exceed available supply."},
		{"спрос", "demand that exceeds supply"},
		{"мощности", "inclusive of higher memory and storage costs"},
		{"найм", "employee voluntary exit offer"},
		{"мощности", "third-party capacity deliveries"},
	} {
		if !has(w.topic, w.part) {
			t.Errorf("no %s signal %q in:\n%v", w.topic, w.part, sigs)
		}
	}
	counts := map[string]int{}
	for _, s := range sigs {
		counts[s.Topic]++
		if !s.Strong || strings.Contains(s.Sentence, "Adam") || strings.Contains(s.Sentence, "agentic hiring") || s.Topic == "FX" {
			t.Errorf("picked: %+v", s)
		}
	}
	for topic, n := range counts {
		if n > 2 {
			t.Errorf("%s: %d signals, limit 2", topic, n)
		}
	}
}

// AMZN Q2 2026 press release: FX is no topic any more; the duplicate check
// spans the company's documents.
func TestSignalsPressRelease(t *testing.T) {
	text := string(read(t, filepath.Join("golden", "AMZN_0001018724-26-000024.txt")))
	sigs := pick(t, func(p *SignalPicker) {
		p.Add(text, "8-K", false)
		p.Add(text, "8-K again", false)
	})
	for _, s := range sigs {
		if strings.Contains(s.Sentence, "foreign exchange") || strings.Contains(s.Sentence, "non-GAAP") || s.Source != "8-K" {
			t.Errorf("picked: %+v", s)
		}
	}
}

// Q&A answers count only with a number or a strong phrase; strong
// sentences win the limits over earlier plain ones.
func TestSignalsQAAndPriority(t *testing.T) {
	sigs := pick(t, func(p *SignalPicker) {
		p.Add("We see strong demand in every region today. Demand remains strong demand for chips overall. We see huge demand from enterprise customers.", "release", false)
		p.Add("And I think that's strong demand, as I said, broadly. Demand continues to exceed supply in the cloud business. We saw strong demand, up 30% across the fleet.", "call, Q&A", true)
	})
	var got []string
	for _, s := range sigs {
		got = append(got, s.Sentence)
	}
	want := []string{"Demand continues to exceed supply in the cloud business.", "We see strong demand in every region today."}
	if !slices.Equal(got, want) {
		t.Errorf("signals = %q, want %q", got, want)
	}
}

// FIXES_W44 D2: within the same strength, higher-priority topics win the
// company limit whatever their order in the text (memory is last).
func TestSignalsTopicPriority(t *testing.T) {
	r := rules(t)
	r.PerCompany = 2
	p := NewSignalPicker(r)
	p.Add("We expect memory costs to rise next year. China sales grew in the quarter. We see strong demand for our products.", "release", false)
	var got []string
	for _, s := range p.Signals() {
		got = append(got, s.Topic)
	}
	if want := []string{"спрос", "Китай"}; !slices.Equal(got, want) {
		t.Errorf("topics = %q, want %q", got, want)
	}
}

// Risk factor lists in a release are fragments without a final stop.
func TestSignalsWholeSentencesOnly(t *testing.T) {
	text := "Risks include:\n• supply or other quality problems;\n• excessive outages, disruptions, or capacity constraints of our services;\nCustomer demand continues to exceed available supply."
	sigs := pick(t, func(p *SignalPicker) { p.Add(text, "8-K", false) })
	if len(sigs) != 1 || sigs[0].Sentence != "Customer demand continues to exceed available supply." {
		t.Errorf("signals = %+v", sigs)
	}
}
