package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Build writes a one-sheet workbook of inline strings (a cell that parses
// as a number is written as a number); for tests of the readers.
func Build(sheet string, rows [][]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			panic(err)
		}
	}
	put("xl/workbook.xml", `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="`+esc(sheet)+`" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	put("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/Sheet1.xml"/></Relationships>`)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, r := range rows {
		fmt.Fprintf(&b, `<row r="%d">`, i+1)
		for j, v := range r {
			if v == "" {
				continue
			}
			ref := fmt.Sprintf("%s%d", colName(j), i+1)
			if isNumber(v) {
				fmt.Fprintf(&b, `<c r="%s"><v>%s</v></c>`, ref, v)
			} else {
				fmt.Fprintf(&b, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, esc(v))
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	put("xl/worksheets/sheet1.xml", b.String())
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func colName(j int) string {
	s := ""
	for j++; j > 0; j = (j - 1) / 26 {
		s = string(rune('A'+(j-1)%26)) + s
	}
	return s
}

func isNumber(v string) bool {
	var f float64
	_, err := fmt.Sscanf(v, "%g", &f)
	return err == nil && strings.TrimSpace(v) == v && !strings.ContainsAny(v, " :/")
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
