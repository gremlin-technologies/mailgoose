package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// emailBlock is the API's EmailBlockEntry; the block stays raw JSON.
type emailBlock struct {
	Block    json.RawMessage `json:"block"`
	Path     string          `json:"path"`
	ParentID *string         `json:"parent_id"`
	Column   *int            `json:"column"`
	Index    int             `json:"index"`
}

type emailBlockList struct {
	EmailID string       `json:"email_id"`
	Version int          `json:"version"`
	Data    []emailBlock `json:"data"`
}

type emailBlockChange struct {
	Block emailBlock `json:"block"`
	Email email      `json:"email"`
}

var blockIDArgPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func blockIDArg(arg string) (string, error) {
	if !blockIDArgPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't a block id.", arg), "`mailgoose email blocks list <email-id>` shows each block's id.")
	}
	return arg, nil
}

func blockPath(emailID, blockID string) string {
	return emailPath(emailID) + "/blocks/" + url.PathEscape(blockID)
}

// blockFieldFlags are the block fields that have a flag of their own. Any
// field, including a columns block's columns and a social block's links,
// can go in --data.
var blockFieldFlags = []struct {
	flag, key, usage string
	isInt            bool
}{
	{"text", "text", "the text of a heading, text, button or footer block", false},
	{"level", "level", "a heading's level, 1 to 3", true},
	{"align", "align", "left, center or right", false},
	{"src", "src", "an image's https URL", false},
	{"alt", "alt", "an image's alt text", false},
	{"href", "href", "where an image or button links to", false},
	{"width", "width", "an image's width in pixels", true},
	{"color", "color", "a divider's color, like #e4e4e7, or a social block's icon color: brand, dark or light", false},
	{"thickness", "thickness", "a divider's thickness in pixels", true},
	{"height", "height", "a spacer's height in pixels", true},
	{"background-color", "background_color", "a button's color", false},
	{"text-color", "text_color", "a button's text color", false},
	{"display", "display", "what a social block shows: icons, icons_and_text or text", false},
	{"size", "size", "a social block's icon size in pixels: 32, 40 or 48", true},
	{"spacing", "spacing", "pixels between a social block's links, 0 to 48", true},
	{"layout", "layout", "a social block's layout: horizontal or vertical", false},
}

func addBlockFieldFlags(fs *pflag.FlagSet) {
	fs.String("data", "", `block fields as a JSON object, like '{"text": "Hi", "level": 2}'; flags override it`)
	fs.StringArray("link", nil, "a social block's link as network=url, like instagram=https://instagram.com/bakery; repeat it, in order (on update, these replace every link)")
	for _, f := range blockFieldFlags {
		if f.isInt {
			fs.Int(f.flag, 0, f.usage)
		} else {
			fs.String(f.flag, "", f.usage)
		}
	}
}

// blockFields gathers --data and the field flags that were set.
func blockFields(fs *pflag.FlagSet) (map[string]any, error) {
	fields := map[string]any{}
	if data, _ := fs.GetString("data"); data != "" {
		dec := json.NewDecoder(strings.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&fields); err != nil || fields == nil || dec.More() {
			return nil, usageError("--data isn't a JSON object.", `Pass the block's fields, like --data '{"text": "Hi"}'.`)
		}
	}
	for _, f := range blockFieldFlags {
		if !fs.Changed(f.flag) {
			continue
		}
		if f.isInt {
			v, _ := fs.GetInt(f.flag)
			fields[f.key] = v
		} else {
			v, _ := fs.GetString(f.flag)
			fields[f.key] = v
		}
	}
	if fs.Changed("link") {
		flags, _ := fs.GetStringArray("link")
		links := make([]any, 0, len(flags))
		for _, l := range flags {
			network, url, ok := strings.Cut(l, "=")
			if !ok || strings.TrimSpace(network) == "" || strings.TrimSpace(url) == "" {
				return nil, usageError(fmt.Sprintf("--link %q isn't network=url.", l),
					"Give a network and its address, like --link instagram=https://instagram.com/bakery. For a label, use --data.")
			}
			links = append(links, map[string]any{"network": strings.TrimSpace(network), "url": strings.TrimSpace(url)})
		}
		fields["links"] = links
	}
	return fields, nil
}

// blockPlaceFlags are where add and move put a block.
type blockPlaceFlags struct {
	after, parent string
	index, column int
}

func (p *blockPlaceFlags) add(fs *pflag.FlagSet, verb string) {
	fs.StringVar(&p.after, "after", "", verb+" right after this block, in the same list")
	fs.IntVar(&p.index, "index", 0, "the place it ends up at, 0 first (top level, or in --in's column)")
	fs.StringVar(&p.parent, "in", "", "a columns block's id, to put it in one of its columns (needs --column)")
	fs.IntVar(&p.column, "column", 0, "with --in: 0 for the left column, 1 for the right")
}

func (p *blockPlaceFlags) set(fs *pflag.FlagSet, body map[string]any) {
	if fs.Changed("after") {
		body["after"] = p.after
	}
	if fs.Changed("index") {
		body["index"] = p.index
	}
	if fs.Changed("in") {
		body["parent_id"] = p.parent
	}
	if fs.Changed("column") {
		body["column"] = p.column
	}
}

// blockPlace says where a block is, for people.
func blockPlace(b emailBlock) string {
	if b.ParentID == nil || b.Column == nil {
		return strconv.Itoa(b.Index + 1)
	}
	side := "left"
	if *b.Column == 1 {
		side = "right"
	}
	return fmt.Sprintf("%s %s %d", *b.ParentID, side, b.Index+1)
}

func blockGist(raw json.RawMessage) (typ, id, gist string) {
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	typ, _ = b["type"].(string)
	id, _ = b["id"].(string)
	for _, k := range []string{"text", "src"} {
		if v, ok := b[k].(string); ok && v != "" {
			gist = strings.ReplaceAll(v, "\n", " ")
			if r := []rune(gist); len(r) > 50 {
				gist = string(r[:49]) + "…"
			}
			return typ, id, strconv.Quote(gist)
		}
	}
	if links, ok := b["links"].([]any); ok {
		var networks []string
		for _, l := range links {
			if m, ok := l.(map[string]any); ok {
				n, _ := m["network"].(string)
				networks = append(networks, n)
			}
		}
		return typ, id, strings.Join(networks, ", ")
	}
	return typ, id, ""
}

func (a *App) printBlockChange(ch emailBlockChange, verb string) {
	if a.jsonOut {
		a.printJSON(ch)
		return
	}
	typ, id, _ := blockGist(ch.Block.Block)
	fmt.Fprintf(a.Stdout, "%s %s block %s at %s. The email is at version %d.\n", verb, typ, id, blockPlace(ch.Block), ch.Email.Version)
}

func (a *App) emailBlocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "blocks",
		Aliases: []string{"block"},
		Short:   "List, add, change, move and remove an email's blocks",
		Long: `Changes one block of a blocks email at a time. Blocks are named by id, which
stays the same through edits and moves; "mailgoose email blocks list" shows
them.

A place is --after a block (in that block's list), --index in the top level,
or --in a columns block with --column 0 (left) or 1 (right) and an optional
--index. With no place, a block goes last: at the top level that's just
before the footer, which always stays the last block.

Each change applies to the email as it is now, unless --version names the
version you read; then it fails with version_conflict if someone saved since.`,
	}

	list := calls("listEmailBlocks", &cobra.Command{
		Use:     "list <email-id>",
		Short:   "List an email's blocks in order, with their ids",
		Example: `  mailgoose email blocks list 3f1c...`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			var out emailBlockList
			if err := a.call(cmd.Context(), http.MethodGet, emailPath(id)+"/blocks", nil, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "PLACE\tID\tTYPE\tCONTENT")
			for _, b := range out.Data {
				typ, bid, gist := blockGist(b.Block)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", blockPlace(b), bid, typ, gist)
			}
			_ = tw.Flush()
			fmt.Fprintf(a.Stdout, "\nVersion %d.\n", out.Version)
			return nil
		},
	})

	var addType, addID string
	var addPlace blockPlaceFlags
	var addVersion int
	add := callsAll([]string{"addEmailBlock", "getEmail"}, &cobra.Command{
		Use:   "add <email-id> --type <type>",
		Short: "Add a block",
		Long: `Adds a block of --type: heading, text, image, button, divider, spacer,
columns or social. Set its fields with flags, or any fields with --data as
JSON; flags win over --data. Fields left out take their defaults. A columns
block starts with two empty columns unless --data gives them. A social block
needs at least one --link; it shows each network's icon (--display icons) in
its own colors (--color brand) at 32 pixels (--size) unless told otherwise.`,
		Example: `  mailgoose email blocks add 3f1c... --type heading --text "Hi"
  mailgoose email blocks add 3f1c... --type button --text "Order now" --href https://bakery.example --after b_k3j9x2m1qa
  mailgoose email blocks add 3f1c... --type columns --index 1
  mailgoose email blocks add 3f1c... --type image --src https://bakery.example/bun.png --in b_cols123456 --column 0
  mailgoose email blocks add 3f1c... --type social --link instagram=https://instagram.com/bakery --link website=https://bakery.example
  mailgoose email blocks add 3f1c... --type social --color dark --size 40 --data '{"links": [{"network": "x", "url": "https://x.com/bakery", "label": "Follow us"}]}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			fs := cmd.Flags()
			block, err := blockFields(fs)
			if err != nil {
				return err
			}
			if addType != "" {
				block["type"] = addType
			}
			if _, ok := block["type"]; !ok {
				return needFlag("type", "A block type")
			}
			if addID != "" {
				block["id"] = addID
			}
			if _, ok := block["columns"]; !ok && block["type"] == "columns" {
				block["columns"] = []any{map[string]any{"blocks": []any{}}, map[string]any{"blocks": []any{}}}
			}
			body := map[string]any{"block": block}
			addPlace.set(fs, body)
			if body["version"], err = a.versionOr(cmd, id, addVersion); err != nil {
				return err
			}
			var out emailBlockChange
			if err := a.call(cmd.Context(), http.MethodPost, emailPath(id)+"/blocks", body, &out, true); err != nil {
				return err
			}
			a.printBlockChange(out, "Added")
			return nil
		},
	})
	af := add.Flags()
	af.StringVar(&addType, "type", "", "heading, text, image, button, divider, spacer, columns or social (required)")
	af.StringVar(&addID, "id", "", "the block's id (default: a new one)")
	addBlockFieldFlags(af)
	addPlace.add(af, "Add it")
	af.IntVar(&addVersion, "version", 0, "the version you read; the change fails if the email changed since")

	var updateVersion int
	update := callsAll([]string{"updateEmailBlock", "getEmail"}, &cobra.Command{
		Use:   "update <email-id> <block-id>",
		Short: "Change a block's fields",
		Long: `Sets the fields given with flags or --data, and leaves the rest. The id and
type can't change; add a new block and remove the old one instead.`,
		Example: `  mailgoose email blocks update 3f1c... b_k3j9x2m1qa --text "Hello again" --align center
  mailgoose email blocks update 3f1c... b_img0000001 --data '{"href": null}'
  mailgoose email blocks update 3f1c... b_social0001 --display icons_and_text --layout vertical`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			blockID, err := blockIDArg(args[1])
			if err != nil {
				return err
			}
			fields, err := blockFields(cmd.Flags())
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return usageError("Nothing to change.", "Pass a field flag such as --text, or --data; `mailgoose email blocks update --help` lists them.")
			}
			body := map[string]any{"fields": fields}
			if body["version"], err = a.versionOr(cmd, id, updateVersion); err != nil {
				return err
			}
			var out emailBlockChange
			if err := a.call(cmd.Context(), http.MethodPatch, blockPath(id, blockID), body, &out, true); err != nil {
				return err
			}
			a.printBlockChange(out, "Updated")
			return nil
		},
	})
	addBlockFieldFlags(update.Flags())
	update.Flags().IntVar(&updateVersion, "version", 0, "the version you read; the change fails if the email changed since")

	var movePlace blockPlaceFlags
	var moveVersion int
	move := callsAll([]string{"moveEmailBlock", "getEmail"}, &cobra.Command{
		Use:   "move <email-id> <block-id>",
		Short: "Move a block, into or out of columns too",
		Long: `Moves a block, and a columns block's contents with it, to the place given.
--index is where it ends up. The footer can't move.`,
		Example: `  mailgoose email blocks move 3f1c... b_k3j9x2m1qa --index 0
  mailgoose email blocks move 3f1c... b_k3j9x2m1qa --after b_p0q9r8s7t6
  mailgoose email blocks move 3f1c... b_k3j9x2m1qa --in b_cols123456 --column 1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			blockID, err := blockIDArg(args[1])
			if err != nil {
				return err
			}
			body := map[string]any{}
			movePlace.set(cmd.Flags(), body)
			if len(body) == 0 {
				return usageError("Move it where?", "Pass --index, --after, or --in with --column.")
			}
			if body["version"], err = a.versionOr(cmd, id, moveVersion); err != nil {
				return err
			}
			var out emailBlockChange
			if err := a.call(cmd.Context(), http.MethodPost, blockPath(id, blockID)+"/move", body, &out, true); err != nil {
				return err
			}
			a.printBlockChange(out, "Moved")
			return nil
		},
	})
	movePlace.add(move.Flags(), "Move it")
	move.Flags().IntVar(&moveVersion, "version", 0, "the version you read; the change fails if the email changed since")

	var removeVersion int
	remove := callsAll([]string{"removeEmailBlock", "getEmail"}, &cobra.Command{
		Use:     "remove <email-id> <block-id>",
		Aliases: []string{"rm"},
		Short:   "Remove a block, and a columns block's contents with it",
		Example: `  mailgoose email blocks remove 3f1c... b_k3j9x2m1qa`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			blockID, err := blockIDArg(args[1])
			if err != nil {
				return err
			}
			v, err := a.versionOr(cmd, id, removeVersion)
			if err != nil {
				return err
			}
			var e email
			if err := a.call(cmd.Context(), http.MethodDelete, blockPath(id, blockID)+"?version="+strconv.Itoa(v), nil, &e, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(e)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Removed block %s. The email is at version %d.\n", blockID, e.Version)
			return nil
		},
	})
	remove.Flags().IntVar(&removeVersion, "version", 0, "the version you read; the change fails if the email changed since")

	cmd.AddCommand(list, add, update, move, remove)
	return cmd
}

// versionOr returns v, or the email's current version when v is 0.
func (a *App) versionOr(cmd *cobra.Command, emailID string, v int) (int, error) {
	if v != 0 {
		return v, nil
	}
	return a.currentVersion(cmd.Context(), emailID)
}
