// Package xlsx reads the cell values of an Office Open XML workbook with
// the standard library (archive/zip, encoding/xml). It reads what the
// Finanzagentur files need: shared and inline strings, numbers as text, the
// sheets by name through the workbook relationships. Formulas give their
// cached value; styles, merged cells and dates are left to the caller
// (Serial converts an Excel date number).
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

// Limits against a broken or hostile file.
const (
	maxPartBytes = 50 << 20 // one unpacked part
	maxRows      = 100_000
	maxCols      = 1_000
)

// Workbook is an opened file.
type Workbook struct {
	zip     *zip.Reader
	sheets  []sheetRef
	strings []string
}

type sheetRef struct{ name, part string }

// Open reads the workbook structure and the shared strings of data.
func Open(data []byte) (*Workbook, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("xlsx: %w", err)
	}
	wb := &Workbook{zip: zr}
	if err := wb.readSheets(); err != nil {
		return nil, err
	}
	if err := wb.readStrings(); err != nil {
		return nil, err
	}
	return wb, nil
}

// Sheets are the sheet names in workbook order.
func (wb *Workbook) Sheets() []string {
	out := make([]string, len(wb.sheets))
	for i, s := range wb.sheets {
		out[i] = s.name
	}
	return out
}

// Rows returns the cell texts of a sheet ("" for the first sheet), one
// slice per row, indexed by column (A = 0); missing rows and cells are
// empty. Rows are numbered from 1 as in Excel: Rows()[0] is row 1.
func (wb *Workbook) Rows(sheet string) ([][]string, error) {
	if len(wb.sheets) == 0 {
		return nil, errors.New("xlsx: no sheets")
	}
	ref := wb.sheets[0]
	if sheet != "" {
		found := false
		for _, s := range wb.sheets {
			if s.name == sheet {
				ref, found = s, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("xlsx: no sheet %q (have %s)", sheet, strings.Join(wb.Sheets(), ", "))
		}
	}
	data, err := wb.part(ref.part)
	if err != nil {
		return nil, err
	}
	var ws struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				Ref    string `xml:"r,attr"`
				Type   string `xml:"t,attr"`
				Value  string `xml:"v"`
				Inline struct {
					Text string    `xml:"t"`
					Runs []runText `xml:"r"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(data, &ws); err != nil {
		return nil, fmt.Errorf("xlsx sheet %q: %w", ref.name, err)
	}
	var out [][]string
	for _, r := range ws.Rows {
		n := r.R
		if n == 0 {
			n = len(out) + 1
		}
		if n > maxRows || n <= len(out) {
			return nil, fmt.Errorf("xlsx sheet %q: bad row number %d", ref.name, r.R)
		}
		for len(out) < n {
			out = append(out, nil)
		}
		row := []string{}
		for j, c := range r.Cells {
			col := j
			if c.Ref != "" {
				if col, err = column(c.Ref); err != nil {
					return nil, fmt.Errorf("xlsx sheet %q: %w", ref.name, err)
				}
			}
			if col < len(row) {
				return nil, fmt.Errorf("xlsx sheet %q: cell %s out of order", ref.name, c.Ref)
			}
			for len(row) < col {
				row = append(row, "")
			}
			var v string
			switch c.Type {
			case "s":
				k, err := strconv.Atoi(strings.TrimSpace(c.Value))
				if err != nil || k < 0 || k >= len(wb.strings) {
					return nil, fmt.Errorf("xlsx sheet %q: cell %s: bad shared string %q", ref.name, c.Ref, c.Value)
				}
				v = wb.strings[k]
			case "inlineStr":
				v = c.Inline.Text + joinRuns(c.Inline.Runs)
			default: // n, b, str, e, d: the cached text
				v = c.Value
			}
			row = append(row, v)
		}
		out[n-1] = row
	}
	return out, nil
}

type runText struct {
	Text string `xml:"t"`
}

func joinRuns(rs []runText) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// column is the 0-based column of a cell reference ("S12" → 18).
func column(ref string) (int, error) {
	n := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A'+1)
	}
	if i == 0 || i == len(ref) || n > maxCols {
		return 0, fmt.Errorf("bad cell reference %q", ref)
	}
	return n - 1, nil
}

func (wb *Workbook) readSheets() error {
	data, err := wb.part("xl/workbook.xml")
	if err != nil {
		return err
	}
	var book struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(data, &book); err != nil {
		return fmt.Errorf("xlsx workbook: %w", err)
	}
	rels, err := wb.rels("xl/_rels/workbook.xml.rels")
	if err != nil {
		return err
	}
	for _, s := range book.Sheets {
		target, ok := rels[s.RID]
		if !ok {
			return fmt.Errorf("xlsx: sheet %q has no relationship %q", s.Name, s.RID)
		}
		wb.sheets = append(wb.sheets, sheetRef{name: s.Name, part: target})
	}
	return nil
}

// rels maps the relationship ids of a .rels part to part names.
func (wb *Workbook) rels(name string) (map[string]string, error) {
	data, err := wb.part(name)
	if err != nil {
		return nil, err
	}
	var r struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("xlsx %s: %w", name, err)
	}
	out := map[string]string{}
	for _, x := range r.Rels {
		t := x.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join("xl", t)
		}
		out[x.ID] = t
	}
	return out, nil
}

func (wb *Workbook) readStrings() error {
	data, err := wb.part("xl/sharedStrings.xml")
	if errors.Is(err, errNoPart) {
		return nil
	}
	if err != nil {
		return err
	}
	var sst struct {
		Items []struct {
			Text string    `xml:"t"`
			Runs []runText `xml:"r"`
		} `xml:"si"`
	}
	if err := xml.Unmarshal(data, &sst); err != nil {
		return fmt.Errorf("xlsx shared strings: %w", err)
	}
	for _, it := range sst.Items {
		wb.strings = append(wb.strings, it.Text+joinRuns(it.Runs))
	}
	return nil
}

var errNoPart = errors.New("xlsx: no such part")

// part reads one file of the archive; names are matched without regard to
// case (the Finanzagentur relationships say Sheet1.xml for sheet1.xml).
func (wb *Workbook) part(name string) ([]byte, error) {
	for _, f := range wb.zip.File {
		if !strings.EqualFold(f.Name, name) {
			continue
		}
		if f.UncompressedSize64 > maxPartBytes {
			return nil, fmt.Errorf("xlsx %s: %d bytes unpacked, over the limit", name, f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("xlsx %s: %w", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxPartBytes+1))
		if err != nil {
			return nil, fmt.Errorf("xlsx %s: %w", name, err)
		}
		if len(data) > maxPartBytes {
			return nil, fmt.Errorf("xlsx %s: over the size limit", name)
		}
		return data, nil
	}
	return nil, fmt.Errorf("%w: %s", errNoPart, name)
}

// Serial converts an Excel date number (1900 system) to a UTC date; the
// fraction of a day is dropped.
func Serial(text string) (time.Time, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || v < 1 || v > 2958465 || math.IsNaN(v) {
		return time.Time{}, fmt.Errorf("not an Excel date: %q", text)
	}
	// Day 60 is the non-existent 1900-02-29; serials after it count from
	// 1899-12-30.
	return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(math.Floor(v))), nil
}
