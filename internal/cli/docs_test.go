package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEveryCommandHasAnExample fails when a command's --help shows no
// example (CLI-5). Groups such as `mailgoose email` only list their
// subcommands, and help topics such as exit-codes run nothing, so only
// runnable commands need one.
func TestEveryCommandHasAnExample(t *testing.T) {
	root := NewApp().rootCmd()
	var walk func(c *cobra.Command)
	checked := 0
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub)
		}
		if c == root || !c.Runnable() {
			return
		}
		checked++
		ex := strings.TrimSpace(c.Example)
		if ex == "" {
			t.Errorf("%q has no example in its --help; add an Example", c.CommandPath())
			return
		}
		// The example shows this very command.
		if !strings.Contains(ex, c.CommandPath()) {
			t.Errorf("%q: its example doesn't show %q", c.CommandPath(), c.CommandPath())
		}
	}
	walk(root)
	if checked < 50 {
		t.Fatalf("only %d commands checked; is the walk broken?", checked)
	}
}

func TestDocsPrintsTheAgentGuide(t *testing.T) {
	h := newHarness(t, "")
	// No API key and no API URL: the guide needs neither.
	if code := h.run("docs"); code != ExitOK {
		t.Fatalf("docs: exit %d, %s", code, h.errOut)
	}
	guide := h.out.String()
	for _, want := range []string{
		"# mailgoose for AI agents",
		"MAILGOOSE_API_KEY",                                        // auth with an API key
		"mailgoose email blocks add",                               // building an email with blocks
		"mailgoose email check",                                    // checks
		"mailgoose campaign test",                                  // test send
		"mailgoose campaign launch <campaign-id> --confirm <code>", // the launch confirmation
		"only after they say yes",
		"## Moving from Mailchimp",               // MG-78
		"mailgoose import mailchimp upload",      // the export
		"mailgoose import mailchimp connect",     // or a connected account
		"mailgoose import mailchimp preview",     // the preview
		"Confirm only after the person says yes", // --permission-confirmed is theirs
		"Segment rules",                          // what doesn't come over
		"automations",
		"reports",
		"signup form designs",
		"Merge fields become contact fields", // MG-132
		"--new-field MERGE",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide lacks %q", want)
		}
	}

	if code := h.run("docs", "--json"); code != ExitOK {
		t.Fatalf("docs --json: exit %d", code)
	}
	var out struct{ Format, Guide string }
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Format != "markdown" || out.Guide != guide {
		t.Errorf("docs --json = %s (err %v)", h.out.String()[:min(80, h.out.Len())], err)
	}
}

// TestGuideNamesRealCommands keeps the guide honest: every command it shows,
// in a code block or a `code` span, must exist.
func TestGuideNamesRealCommands(t *testing.T) {
	known := map[string]bool{}
	for _, c := range NewApp().Commands() {
		known[c.Command] = true
	}
	known["help exit-codes"] = true

	var shown []string
	inBlock := false
	for _, line := range strings.Split(AgentGuide, "\n") {
		if strings.HasPrefix(line, "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock {
			if rest, ok := strings.CutPrefix(line, "mailgoose "); ok {
				shown = append(shown, rest)
			}
			continue
		}
		for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
			shown = append(shown, m[1])
		}
	}
	if len(shown) < 20 {
		t.Fatalf("only %d commands found in the guide", len(shown))
	}
	for _, cmd := range shown {
		words := strings.Fields(cmd)
		found := false
		for n := min(3, len(words)); n > 0; n-- {
			if known[strings.Join(words[:n], " ")] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the guide shows an unknown command: mailgoose %s", cmd)
		}
	}
}

var codeSpan = regexp.MustCompile("`mailgoose ([^`]+)`")
