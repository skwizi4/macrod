package xlsx

import (
	"testing"
	"time"
)

func TestBuildAndRead(t *testing.T) {
	data := Build("Data", [][]string{{"Name", "", "Value"}, nil, {"a & b", "", "1.5"}})
	wb, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := wb.Sheets(); len(got) != 1 || got[0] != "Data" {
		t.Fatalf("sheets = %v", got)
	}
	rows, err := wb.Rows("Data")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][2] != "Value" || len(rows[1]) != 0 || rows[2][0] != "a & b" || rows[2][2] != "1.5" {
		t.Fatalf("rows = %q", rows)
	}
	if _, err := wb.Rows("Other"); err == nil {
		t.Error("unknown sheet: no error")
	}
}

func TestOpenRejectsNonZip(t *testing.T) {
	if _, err := Open([]byte("not a workbook")); err == nil {
		t.Fatal("no error")
	}
}

func TestSerial(t *testing.T) {
	for in, want := range map[string]string{"46335": "2026-11-09", "36166": "1999-01-06", "46027.75": "2026-01-05"} {
		got, err := Serial(in)
		if err != nil || got.Format(time.DateOnly) != want {
			t.Errorf("Serial(%s) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "0", "-3"} {
		if _, err := Serial(bad); err == nil {
			t.Errorf("Serial(%q): no error", bad)
		}
	}
}

func TestColumn(t *testing.T) {
	for in, want := range map[string]int{"A1": 0, "S12": 18, "AA3": 26} {
		if got, err := column(in); err != nil || got != want {
			t.Errorf("column(%s) = %d, %v", in, got, err)
		}
	}
	if _, err := column("12"); err == nil {
		t.Error("no letters: no error")
	}
}
