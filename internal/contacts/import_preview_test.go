package contacts

import (
	"strings"
	"testing"
)

// The preview counts rows up to the import's limit and reports going past
// it, without needing a database.
func TestPreviewImportRowLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("email\n")
	for i := 0; i <= MaxImportRows; i++ {
		b.WriteString("a@example.com\n")
	}
	p, err := PreviewImport(ImportInput{CSV: []byte(b.String())})
	if err != nil {
		t.Fatal(err)
	}
	if p.Problem == nil || p.Problem.Code != "too_many_rows" || p.Rows != MaxImportRows+1 {
		t.Errorf("problem %+v, rows %d", p.Problem, p.Rows)
	}

	big := make([]byte, MaxImportBytes+1)
	if _, err := PreviewImport(ImportInput{CSV: big}); err == nil || !strings.Contains(err.Error(), "over 10 MB") {
		t.Errorf("over 10 MB: %v", err)
	}
}

// A column is stored by position when its header is blank or repeated, and
// found again by that name.
func TestColumnNames(t *testing.T) {
	l, err := readLayout([]byte("Email,,Name,Name,#9\na@example.com,x,A,B,y\n"), true,
		columnChoice{named: map[string]string{fieldFirstName: "#4", fieldLastName: "#9"}, guess: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := range l.header {
		names = append(names, l.name(i))
	}
	if got := strings.Join(names, " "); got != "Email #2 Name #4 #9" {
		t.Errorf("names = %s", got)
	}
	// "#9" is a header here, so it names that column, not a ninth one.
	if l.col[fieldFirstName] != 3 || l.col[fieldLastName] != 4 || *l.column(fieldFirstName) != "#4" {
		t.Errorf("cols = %v", l.col)
	}
	// A header that is literally another column's position name goes by its
	// own position, so each name finds one column.
	l, err = readLayout([]byte("#2,,Email\nx,Pat,a@example.com\n"), true, columnChoice{named: map[string]string{fieldFirstName: "#2"}, guess: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.name(0) + " " + l.name(1); got != "#1 #2" || l.col[fieldFirstName] != 1 {
		t.Errorf("names = %s, cols = %v", got, l.col)
	}
	if _, err := readLayout([]byte("email\na@example.com\n"), true, columnChoice{named: map[string]string{fieldFirstName: "#2"}}); err == nil {
		t.Error("#2 of one column was accepted")
	}
}

// Pasted contacts without a header (MG-125): the first row is a contact,
// columns go by position, and the email column is found from the first
// row's addresses.
func TestPreviewWithoutHeader(t *testing.T) {
	for _, tc := range []struct {
		name, csv, sep string
	}{
		{"tab, as copied from a spreadsheet", "Pat\tpat@example.com\tLee\nSam\tsam@example.com\tRo\n", "tab"},
		{"comma", "Pat,pat@example.com,Lee\nSam,sam@example.com,Ro\n", "comma"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PreviewImport(ImportInput{CSV: []byte(tc.csv), NoHeader: true})
			if err != nil {
				t.Fatal(err)
			}
			if p.Problem != nil || p.Separator != tc.sep || p.Rows != 2 || p.HasHeaderGuess {
				t.Fatalf("problem %+v, separator %s, rows %d, guess %v", p.Problem, p.Separator, p.Rows, p.HasHeaderGuess)
			}
			var names, fields []string
			for _, c := range p.Columns {
				names = append(names, c.Name+"="+c.Header)
				fields = append(fields, c.Field)
			}
			if got := strings.Join(names, " "); got != "#1= #2= #3=" {
				t.Errorf("names %s", got)
			}
			if fields[1] != "email" || fields[0] != "" {
				t.Errorf("fields %q", fields)
			}
			if got := strings.Join(p.Columns[1].Samples, " "); got != "pat@example.com sam@example.com" {
				t.Errorf("samples %s", got)
			}
		})
	}

	// Naming columns by position.
	p, err := PreviewImport(ImportInput{CSV: []byte("Pat,pat@example.com\n"), NoHeader: true, EmailColumn: "#2", FirstNameColumn: "#1"})
	if err != nil || p.Problem != nil || p.Columns[0].Field != "first_name" || p.Columns[1].Field != "email" {
		t.Fatalf("named: %v %+v", err, p)
	}
	// No address in the first row: the email column has to be chosen.
	p, err = PreviewImport(ImportInput{CSV: []byte("Pat,Lee\n"), NoHeader: true})
	if err != nil || p.Problem == nil || p.Problem.Code != "no_email_column" || strings.Contains(p.Problem.Message, "header") {
		t.Fatalf("no email: %v %+v", err, p.Problem)
	}
}

// The header guess: no header when the first row holds an address where
// email would come from.
func TestHeaderGuess(t *testing.T) {
	for _, tc := range []struct {
		csv  string
		want bool
	}{
		{"email,name\npat@example.com,Pat\n", true},
		{"Email Address\tName\npat@example.com\tPat\n", true},
		{"pat@example.com\tPat\nsam@example.com\tSam\n", false},
		{"Pat,pat@example.com\n", false},
		{"first,last\nPat,Lee\n", true},
	} {
		p, err := PreviewImport(ImportInput{CSV: []byte(tc.csv)})
		if err != nil {
			t.Fatal(err)
		}
		if p.HasHeaderGuess != tc.want {
			t.Errorf("%q: guess %v", tc.csv, p.HasHeaderGuess)
		}
	}
}
