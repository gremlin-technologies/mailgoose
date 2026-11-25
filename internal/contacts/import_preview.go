package contacts

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

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
	Rows int
	// HasHeaderGuess is whether the first row looks like a header (MG-125):
	// false when it holds an email address in the column email would come
	// from, or in any column when no header names one.
	HasHeaderGuess bool
	Columns        []PreviewColumn
	// Problem is why CreateImport would refuse this file and mapping; nil
	// when it would take it.
	Problem *auth.ValidationError
}

// PreviewColumn is one column of a previewed file.
type PreviewColumn struct {
	// Position counts from 1.
	Position int
	// Header is blank for a file without a header row.
	Header string
	// Name is how to name the column in an import's columns: its header,
	// or "#3" for one whose header is blank or repeated.
	Name string
	// Field is the field the column would become, empty when it isn't
	// imported or becomes a contact field.
	Field string
	// CustomField is the account's contact field the column would fill,
	// named or matched by its header (MG-131); nil when none.
	CustomField *Field
	// NewField is the contact field the import would make from the column;
	// nil when none.
	NewField *NewFieldPreview
	// Samples are the column's values in the first rows, "" where a row
	// has none.
	Samples []string
}

// NewFieldPreview is a contact field an import would make.
type NewFieldPreview struct {
	Name, Key string
}

var separatorNames = map[rune]string{',': "comma", ';': "semicolon", '\t': "tab"}

// PreviewImport is PreviewFile with the account's contact fields.
func (s *Service) PreviewImport(ctx context.Context, accountID string, in ImportInput) (*ImportPreview, error) {
	af, err := s.accountFields(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return PreviewFile(in, af)
}

// PreviewFile reads a file the way CreateImport would with the same
// columns, for an account with the contact fields af, and changes nothing.
// It returns an error only when the file can't be read at all; a problem the
// import would refuse it for, such as no email column or too many new
// fields, is in the preview's Problem.
func PreviewFile(in ImportInput, af AccountFields) (*ImportPreview, error) {
	if len(in.CSV) > MaxImportBytes {
		return nil, invalidImport("file_too_large", "csv", fmt.Sprintf("The file is over %d MB. Split it into smaller files.", MaxImportBytes>>20))
	}
	l, err := readHeader(in.CSV, !in.NoHeader)
	if err != nil {
		return nil, err
	}
	p := &ImportPreview{Separator: separatorNames[l.comma], HasHeaderGuess: guessHeader(in)}
	problem := l.choose(in.choice(af))
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
	custom := map[int]*Field{}
	for _, f := range af.Fields {
		if i, ok := l.fieldCol[f.ID]; ok {
			custom[i] = f
		}
	}
	made := map[int]*NewFieldPreview{}
	for _, nf := range l.newCols {
		made[nf.col] = &NewFieldPreview{Name: nf.name, Key: nf.key}
	}
	p.Columns = make([]PreviewColumn, len(l.header))
	for i, h := range l.header {
		c := PreviewColumn{Position: i + 1, Header: h, Name: l.name(i), Field: field[i], CustomField: custom[i], NewField: made[i],
			Samples: make([]string, len(samples))}
		for r, rec := range samples {
			c.Samples[r] = unescapeCell(cell(rec, i))
		}
		p.Columns[i] = c
	}
	return p, nil
}

var errStopCounting = errors.New("stop counting")

// guessHeader reports whether a file's first row looks like a header: it
// doesn't, when it holds an email address where email would come from if
// it were the header, or anywhere when no header would name an email
// column.
func guessHeader(in ImportInput) bool {
	l, err := readHeader(in.CSV, true)
	if err != nil {
		return true
	}
	_ = l.choose(in.choice(AccountFields{}))
	isEmail := func(v string) bool {
		_, err := NormalizeEmail(unescapeCell(strings.TrimSpace(v)))
		return err == nil
	}
	if i := l.col[fieldEmail]; i >= 0 {
		return !isEmail(l.header[i])
	}
	return !slices.ContainsFunc(l.header, isEmail)
}

// sampleRows returns up to n of the first contact rows, cell by cell.
// Blank rows and rows that can't be read are left out.
func sampleRows(data []byte, l *csvLayout, n int) ([][]string, error) {
	r := newCSVReader(data, l.comma)
	r.ReuseRecord = false
	if l.hasHeader {
		if _, err := r.Read(); err != nil {
			return nil, invalidImport("invalid_csv", "csv", "The header row can't be read as CSV: "+err.Error())
		}
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
