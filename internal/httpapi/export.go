package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// exportXLSX turns a report table built by the web app into an Excel workbook.
// The data is what the user is already allowed to see (the browser sends it back); nothing is read from the database.
func (s *Server) exportXLSX(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		RTL      bool   `json:"rtl"`
		Columns  []struct {
			Label   string `json:"label"`
			Numeric bool   `json:"numeric"`
		} `json:"columns"`
		Rows   [][]any `json:"rows"`
		Footer [][]any `json:"footer"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Columns) == 0 || len(in.Columns) > 60 || len(in.Rows)+len(in.Footer) > 200000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Paxve"
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := sheetName(title)
	_ = f.SetSheetName("Sheet1", sheet)
	if in.RTL {
		rtl := true
		_ = f.SetSheetView(sheet, 0, &excelize.ViewOptions{RightToLeft: &rtl})
	}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	head, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1F7A4D"}}})
	num, _ := f.NewStyle(&excelize.Style{NumFmt: 4}) // #,##0.00
	numBold, _ := f.NewStyle(&excelize.Style{NumFmt: 4, Font: &excelize.Font{Bold: true}})
	txtBold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})

	row := 1
	_ = f.SetCellValue(sheet, "A1", title)
	_ = f.SetCellStyle(sheet, "A1", "A1", bold)
	row++
	if sub := strings.TrimSpace(in.Subtitle); sub != "" {
		_ = f.SetCellValue(sheet, "A2", sub)
		row++
	}
	row++
	for c, col := range in.Columns {
		cell, _ := excelize.CoordinatesToCellName(c+1, row)
		_ = f.SetCellValue(sheet, cell, col.Label)
		_ = f.SetCellStyle(sheet, cell, cell, head)
		width := float64(len([]rune(col.Label)) + 4)
		if col.Numeric && width < 16 {
			width = 16
		} else if width < 14 {
			width = 14
		}
		name, _ := excelize.ColumnNumberToName(c + 1)
		_ = f.SetColWidth(sheet, name, name, width)
	}
	headRow := row
	write := func(cells []any, footer bool) {
		row++
		for c := 0; c < len(in.Columns) && c < len(cells); c++ {
			cell, _ := excelize.CoordinatesToCellName(c+1, row)
			v := cells[c]
			if in.Columns[c].Numeric {
				if n, ok := toNumber(v); ok {
					_ = f.SetCellValue(sheet, cell, n)
					if footer {
						_ = f.SetCellStyle(sheet, cell, cell, numBold)
					} else {
						_ = f.SetCellStyle(sheet, cell, cell, num)
					}
					continue
				}
			}
			if v == nil {
				continue
			}
			_ = f.SetCellValue(sheet, cell, fmt.Sprint(v))
			if footer {
				_ = f.SetCellStyle(sheet, cell, cell, txtBold)
			}
		}
	}
	for _, cells := range in.Rows {
		write(cells, false)
	}
	for _, cells := range in.Footer {
		write(cells, true)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: headRow, TopLeftCell: fmt.Sprintf("A%d", headRow+1), ActivePane: "bottomLeft"})
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	name := fileSafe.ReplaceAllString(title, "-") + "-" + time.Now().Format("2006-01-02") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

var fileSafe = regexp.MustCompile(`[^\p{L}\p{N}_.-]+`)

func sheetName(title string) string {
	s := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, title)
	if rs := []rune(s); len(rs) > 31 {
		s = string(rs[:31])
	}
	return s
}

func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		t := strings.ReplaceAll(strings.TrimSpace(x), ",", "")
		if t == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(t, 64)
		return n, err == nil
	}
	return 0, false
}
