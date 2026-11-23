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
	l, err := readLayout([]byte("Email,,Name,Name,#9\na@example.com,x,A,B,y\n"),
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
	if _, err := readLayout([]byte("email\na@example.com\n"), columnChoice{named: map[string]string{fieldFirstName: "#2"}}); err == nil {
		t.Error("#2 of one column was accepted")
	}
}
