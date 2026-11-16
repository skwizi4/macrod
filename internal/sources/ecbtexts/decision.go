package ecbtexts

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Decision actions.
const (
	ActionRaise    = "raise"
	ActionLower    = "lower"
	ActionMaintain = "maintain"
)

// Decision is what a "Monetary policy decisions" release says about the
// key ECB rates. Parsed is false unless both the action and the three
// rates were read: the wording changes from time to time, and a decision
// that is not understood must show as such, never as a guessed number.
type Decision struct {
	Parsed        bool
	Action        string  // raise | lower | maintain; "" if not read
	MoveBP        int     // size of a change; 0 for maintain
	DFR, MRO, MLF float64 // deposit facility, main refinancing operations, marginal lending facility, %
	EffectiveFrom time.Time
}

var (
	keepRates  = regexp.MustCompile(`(?i)decided to keep the (?:three )?key ECB interest rates unchanged`)
	moveRates  = regexp.MustCompile(`(?i)decided to (raise|increase|lower|reduce|cut|decrease) the (?:three )?key ECB interest rates by (\d+) basis points`)
	moveDFR    = regexp.MustCompile(`(?i)decided to (raise|increase|lower|reduce|cut|decrease) the deposit facility rate[^.]*? by (\d+) basis points`)
	rateLevels = regexp.MustCompile(`(?i)interest rates on the deposit facility, the main refinancing operations and the marginal lending facility will (?:remain unchanged at|be (?:increased|decreased|raised|lowered|reduced|cut) to) (\d+\.\d+) ?%, (\d+\.\d+) ?% and (\d+\.\d+) ?%,? respectively`)
	effective  = regexp.MustCompile(`with effect from (\d{1,2} [A-Z][a-z]+ \d{4})`)
)

// ParseDecision reads a decision release body.
func ParseDecision(body string) Decision {
	var d Decision
	switch {
	case keepRates.MatchString(body):
		d.Action = ActionMaintain
	default:
		m := moveRates.FindStringSubmatch(body)
		if m == nil {
			m = moveDFR.FindStringSubmatch(body)
		}
		if m != nil {
			d.Action = direction(m[1])
			d.MoveBP, _ = strconv.Atoi(m[2])
		}
	}
	if m := rateLevels.FindStringSubmatch(body); m != nil {
		d.DFR, _ = strconv.ParseFloat(m[1], 64)
		d.MRO, _ = strconv.ParseFloat(m[2], 64)
		d.MLF, _ = strconv.ParseFloat(m[3], 64)
		d.Parsed = d.Action != ""
	}
	if m := effective.FindStringSubmatch(body); m != nil {
		d.EffectiveFrom, _ = time.Parse("2 January 2006", m[1])
	}
	return d
}

func direction(verb string) string {
	switch strings.ToLower(verb) {
	case "raise", "increase":
		return ActionRaise
	default:
		return ActionLower
	}
}
