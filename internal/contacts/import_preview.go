package contacts

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// PreviewSampleRows is how many rows a preview shows of each column.
const PreviewSampleRows = 3

// ImportPreview is what an import would read from a file, before it's
// started (MG-124): the separator, each column with a few of its values and
// the field it would become, and the first problem the import would refuse
// the file for.
type ImportPreview struct {
	// Separator is comma, semicolon or tab.
	Separator string
	// Rows counts the contact rows, not counting the header or blank rows.
	// Past MaxImportRows it stops counting.
	Rows    int
	Columns []PreviewColumn
	// Problem is why CreateImport would refuse this file and mapping; nil
	// when it would take it.
	Problem *auth.ValidationError
}

// PreviewColumn is one column of a previewed file.
type PreviewColumn struct {
	// Position counts from 1.
	Position int
	Header   string
	// Name is how to name the column in an import's columns: its header,
	// or "#3" for one whose header is blank or repeated.
	Name string
	// Field is the field the column would become, empty when it isn't
	// imported.
	Field string
	// Samples are the column's values in the first rows, "" where a row
	// has none.
	Samples []string
}

var separatorNames = map[rune]string{',': "comma", ';': "semicolon", '\t': "tab"}

// PreviewImport reads a file the way CreateImport would with the same
// columns, and changes nothing. It returns an error only when the file can't
// be read at all; a problem the import would refuse it for, such as no email
// column, is in the preview's Problem.
func PreviewImport(in ImportInput) (*ImportPreview, error) {
	if len(in.CSV) > MaxImportBytes {
		return nil, invalidImport("file_too_large", "csv", fmt.Sprintf("The file is over %d MB. Split it into smaller files.", MaxImportBytes>>20))
	}
	l, err := readHeader(in.CSV)
	if err != nil {
		return nil, err
	}
	p := &ImportPreview{Separator: separatorNames[l.comma]}
	problem := l.choose(in.choice())
	if problem == nil {
		p.Rows, problem = checkRows(in.CSV, l, nil)
	} else {
		// Count the rows without the field checks, which need a valid
		// mapping.
		_ = l.eachRow(in.CSV, func(csvRow) error {
			if p.Rows++; p.Rows > MaxImportRows {
				return errStopCounting
			}
			return nil
		})
	}
	if problem != nil {
		var ve *auth.ValidationError
		if !errors.As(problem, &ve) {
			return nil, problem
		}
		p.Problem = ve
	}

	samples, err := sampleRows(in.CSV, l, PreviewSampleRows)
	if err != nil {
		return nil, err
	}
	field := map[int]string{}
	for f, i := range l.col {
		if i >= 0 {
			field[i] = f
		}
	}
	p.Columns = make([]PreviewColumn, len(l.header))
	for i, h := range l.header {
		c := PreviewColumn{Position: i + 1, Header: h, Name: l.name(i), Field: field[i], Samples: make([]string, len(samples))}
		for r, rec := range samples {
			c.Samples[r] = unescapeCell(cell(rec, i))
		}
		p.Columns[i] = c
	}
	return p, nil
}

var errStopCounting = errors.New("stop counting")

// sampleRows returns up to n of the first contact rows, cell by cell.
// Blank rows and rows that can't be read are left out.
func sampleRows(data []byte, l *csvLayout, n int) ([][]string, error) {
	r := newCSVReader(data, l.comma)
	r.ReuseRecord = false
	if _, err := r.Read(); err != nil {
		return nil, invalidImport("invalid_csv", "csv", "The header row can't be read as CSV: "+err.Error())
	}
	var out [][]string
	for len(out) < n {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		var pe *csv.ParseError
		if errors.As(err, &pe) && (!errors.Is(pe.Err, csv.ErrQuote) || pe.Line <= pe.StartLine) {
			// A row with a stray quote isn't shown.
			continue
		}
		if err != nil {
			// A quote that never closes ends the samples.
			break
		}
		if blank(rec) {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
