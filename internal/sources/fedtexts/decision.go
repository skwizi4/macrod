package fedtexts

import (
	"regexp"
	"strconv"
	"strings"
)

// Policy actions of an FOMC statement.
const (
	ActionRaise    = "raise"
	ActionLower    = "lower"
	ActionMaintain = "maintain"
)

// Decision is the rate decision and the vote read from an FOMC statement
// (FIXES_W44 B5). Zero fields were not found.
type Decision struct {
	Action   string // ActionRaise | ActionLower | ActionMaintain
	MoveBP   int    // size of a raise or lower, basis points
	Range    string // target range after the decision: "3.75–4.00%"
	For      int    // votes for; with Against, 0/0 when the vote is not stated
	Against  int
	Dissents []Dissent
}

// Dissent is a group of members voting against with the same preference.
type Dissent struct {
	Names     []string
	Preferred string // ActionRaise | ActionLower | ActionMaintain, "" if not read
	MoveBP    int
}

var (
	decidedRe = regexp.MustCompile(`(?i)decided to (raise|lower|maintain) the target range for the federal funds rate(?: by ([\d/]+) percentage point)?(?: to| at) ([\d/-]+) to ([\d/-]+) percent`)
	voteRe    = regexp.MustCompile(`(?i)by an? (\d+)\s*[–-]\s*(\d+) vote`)
	forRe     = regexp.MustCompile(`(?im)^Voting for (?:the|this) (?:monetary policy )?action were (.+)$`)
	againstRe = regexp.MustCompile(`(?im)Voting against (?:the|this) (?:monetary policy )?action (?:was|were) (.+?), who preferred (.+?)(?:\.\s|\.?$)`)
	moveRe    = regexp.MustCompile(`(?i)\bby ([\d/]+) percentage point`)
	prefRe    = regexp.MustCompile(`(?i)\b(raise|lower|maintain)\b|no change`)
)

// ParseDecision reads the decision and the vote from a statement body.
func ParseDecision(body string) Decision {
	// One paragraph per line, spaces collapsed: a voter list ends with its
	// paragraph, not at "Jerome H." .
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	text := strings.Join(lines, "\n")
	var d Decision
	if m := decidedRe.FindStringSubmatch(text); m != nil {
		d.Action = strings.ToLower(m[1])
		d.MoveBP = fractionBP(m[2])
		lo, hi := fraction(m[3]), fraction(m[4])
		if lo >= 0 && hi >= 0 {
			d.Range = strconv.FormatFloat(lo, 'f', 2, 64) + "–" + strconv.FormatFloat(hi, 'f', 2, 64) + "%"
		}
	}
	for _, m := range againstRe.FindAllStringSubmatch(text, -1) {
		x := Dissent{Names: names(m[1])}
		if p := prefRe.FindString(m[2]); p != "" {
			x.Preferred = strings.ToLower(p)
			if x.Preferred == "no change" {
				x.Preferred = ActionMaintain
			}
		}
		if mv := moveRe.FindStringSubmatch(m[2]); mv != nil {
			x.MoveBP = fractionBP(mv[1])
		}
		d.Dissents = append(d.Dissents, x)
		d.Against += len(x.Names)
	}
	switch m := voteRe.FindStringSubmatch(text); {
	case m != nil:
		d.For, _ = strconv.Atoi(m[1])
		d.Against, _ = strconv.Atoi(m[2])
	default:
		// Older statements list the voters instead: "A, Chair; B; and C."
		if f := forRe.FindStringSubmatch(text); f != nil {
			d.For = len(strings.Split(f[1], ";"))
		}
	}
	return d
}

// names splits "A, B, and C" / "A and B".
func names(s string) []string {
	s = strings.ReplaceAll(s, ", and ", ", ")
	s = strings.ReplaceAll(s, " and ", ", ")
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// fraction reads "3-3/4", "4", "1/2"; −1 if it cannot.
func fraction(s string) float64 {
	whole, frac, ok := strings.Cut(s, "-")
	if !ok {
		if strings.Contains(s, "/") {
			whole, frac = "0", s
		} else {
			frac = ""
		}
	}
	w, err := strconv.Atoi(whole)
	if err != nil {
		return -1
	}
	v := float64(w)
	if frac != "" {
		n, dn, ok := strings.Cut(frac, "/")
		a, err1 := strconv.Atoi(n)
		b, err2 := strconv.Atoi(dn)
		if !ok || err1 != nil || err2 != nil || b == 0 {
			return -1
		}
		v += float64(a) / float64(b)
	}
	return v
}

// fractionBP reads a move in percentage points ("1/4") as basis points.
func fractionBP(s string) int {
	if s == "" {
		return 0
	}
	v := fraction(s)
	if v < 0 {
		return 0
	}
	return int(v*100 + 0.5)
}
