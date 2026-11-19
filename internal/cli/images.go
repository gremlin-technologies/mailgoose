package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// maxImageFile is the largest image the API takes.
const maxImageFile = 5 << 20

// image is the API's Image.
type image struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	FileName    string    `json:"file_name"`
	ContentType string    `json:"content_type"`
	Bytes       int       `json:"bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

type imagePage struct {
	Data       []image `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.Itoa((n+512)>>10) + " KB"
	default:
		return strconv.Itoa(n) + " B"
	}
}

func (a *App) imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Upload pictures for image blocks",
		Long: `Upload pictures for image blocks. An upload answers with the picture's URL,
which goes in an image block's src:

  mailgoose image upload ./logo.png
  mailgoose email blocks add <email-id> --type image --src <url> --alt "Our logo"

JPEG, PNG, GIF and WebP up to 5 MB are accepted, checked by what the file
holds, not its name.`,
	}

	upload := calls("uploadImage", &cobra.Command{
		Use:   "upload <path>",
		Short: "Upload a picture and print its URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			hint := "Pass the path of a JPEG, PNG, GIF or WebP file."
			f, err := os.Open(path) //nolint:gosec // the file the user named
			if err != nil {
				return usageError("Can't read "+path+": "+err.Error(), hint)
			}
			defer func() { _ = f.Close() }()
			data, err := io.ReadAll(io.LimitReader(f, maxImageFile+1))
			if err != nil {
				return usageError("Can't read "+path+": "+err.Error(), hint)
			}
			if len(data) > maxImageFile {
				return usageError(path+" is over 5 MB.", "Resize or compress it; a picture in an email rarely needs to be wider than 1200 pixels.")
			}
			body := map[string]any{"data": data, "file_name": filepath.Base(path)}
			var img image
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/images", body, &img, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(img)
				return nil
			}
			fmt.Fprintln(a.Stdout, img.URL)
			return nil
		},
	})

	var cursor string
	var limit int
	list := calls("listImages", &cobra.Command{
		Use:   "list",
		Short: "List uploaded pictures, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			path := "/v1/images"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page imagePage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No images yet. `mailgoose image upload <path>` adds one.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "FILE\tSIZE\tUPLOADED\tURL")
			for _, img := range page.Data {
				name := img.FileName
				if name == "" {
					name = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, sizeText(img.Bytes), img.CreatedAt.Local().Format("2006-01-02 15:04"), img.URL)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore: mailgoose image list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	list.Flags().IntVar(&limit, "limit", 0, "images per page, 1 to 100")

	cmd.AddCommand(upload, list)
	return cmd
}
