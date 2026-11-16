package htmltext

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestText(t *testing.T) {
	const page = `<div id="body">
<h3>Title&nbsp; here</h3>
<script>var x = 1;</script><style>p { color: red }</style>
<p>First   line<br>second
line</p>
<ul><li>one</li><li>two</li></ul>
<table><tr><th>Term</th><th>Amount</th></tr><tr><td> 10-year </td><td>$42 billion</td></tr></table>
<p>   </p>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	sel := doc.Find("#body")
	want := "Title here\n\nFirst line\nsecond line\n\none\n\ntwo\n\nTerm | Amount\n\n10-year | $42 billion"
	if got := Text(sel); got != want {
		t.Errorf("Text =\n%q\nwant\n%q", got, want)
	}
	if sel.Find("script").Length() != 1 {
		t.Error("Text modified the selection")
	}
}

func TestClean(t *testing.T) {
	if got := Clean("  a  b\n\tc  "); got != "a b c" {
		t.Errorf("Clean = %q", got)
	}
}
