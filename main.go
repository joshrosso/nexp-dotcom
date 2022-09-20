package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	na "github.com/jomei/notionapi"
	"github.com/joshrosso/nexp/config"
	ne "github.com/joshrosso/nexp/export"
	"gopkg.in/yaml.v3"
)

const (
	notionApiEnvVar = "NOTION_TOKEN"
	dbID            = "864350bdeb5e42358b3e05accccc0e3f"
	statusKey       = "Status"
	titleKey        = "Name"
	selectType      = "select"
	onlineKey       = "online"
	mdExtension     = ".md"
)

func main() {
	pageRefresh := make(map[string]time.Time)
	for {
		time.Sleep(5 * time.Second)
		token, err := resolveNotionToken()
		if err != nil {
			fmt.Printf("Failed to resolve token. Error: %s\n", err)
			continue
		}
		c := na.NewClient(na.Token(token))

		resp, err := c.Database.Query(context.Background(), na.DatabaseID(dbID), &na.DatabaseQueryRequest{})
		if err != nil {
			fmt.Printf("Failed to query database. Error: %s\n", err)
			continue
		}

		for _, r := range resp.Results {
			if statusValue, ok := r.Properties[statusKey]; ok {
				if statusValue.GetType() != selectType {
					fmt.Printf("Unexpectedly found incorrect type for Status property on page %s. Expected %s found %s.\n", r.ID, selectType, statusValue.GetType())
					continue
				}
				status := statusValue.(*na.SelectProperty).Select.Name

				if status != onlineKey {
					fmt.Printf("Skipping page %s because status is \"%s\" but needs to be \"%s\".\n", r.ID, status, onlineKey)
					continue
				}
			} else {
				fmt.Printf("Skipping post with ID %s because statusKey missing\n", r.ID)
				continue
			}

			title := r.Properties[titleKey].(*na.TitleProperty).Title[0].PlainText
			if pageRefresh[string(r.ID)] != r.LastEditedTime {
				fmt.Printf("Page qualified to render. ID: %s || Title: %s\n", r.ID, title)
				// Mitigates the chance of a race condition where edit has been
				// updated but notion hasn't persisted all the content changes.
				//time.Sleep(20 * time.Second)
				RenderPage(string(r.ID), filepath.Join("out", sanatizeTitleForFileName(title))+mdExtension)
				pageRefresh[string(r.ID)] = r.LastEditedTime
			} else {
				fmt.Printf("No updates on %s. LastEditedTime: %s, Recorded Time: %s\n", r.ID, r.LastEditedTime, pageRefresh[string(r.ID)])
			}
		}
	}
}

func sanatizeTitleForFileName(title string) string {
	t := strings.ReplaceAll(title, " ", "-")
	t = strings.ReplaceAll(t, "/", "")
	t = strings.ReplaceAll(t, "(", "")
	t = strings.ReplaceAll(t, ")", "")
	t = strings.ReplaceAll(t, "\\", "")
	t = strings.ReplaceAll(t, "'", "")
	t = strings.ToLower(t)
	return t
}

type headerMeta struct {
	Title       string
	Description string
	Date        string
	Images      []string
	Aliases     []string
}

func imageOverride(b *ne.Block) (string, error) {
	if b.BlockRef.GetType() != "image" {
		return "", fmt.Errorf("Failed to donwload image, block was of type %s.", b.BlockRef.GetType())
	}

	config := resolveRenderConfig(b.Opts...)
	ib := b.BlockRef.(*na.ImageBlock)

	// image was not uploaded to Notion, but is referenced from an
	// external URL.
	if ib.Image.External != nil {
		// TODO(joshrosso): Friendly name is currently "image". Should think
		// about how to make this more eloquent.
		return fmt.Sprintf(ne.MdImagePattern, "image", ib.Image.External.URL), nil
	}

	title := sanatizeTitleForFileName(ne.ResolveTitleInPage(b.PageRef))
	// image was uploaded to Notion, need to download to local
	// filesystem.
	var filePath string
	var err error

	// where on my server to save images
	config.ImageOpts.SavePath = filepath.Join(string(filepath.Separator), "usr", "share", "server", "files", "img", "posts", title)
	if ib.Image.File != nil {
		filePath, err = ne.SaveNotionImageToFilesystem(ib.Image.File.URL, config.ImageOpts)
		if err != nil {
			return "", err
		}
	}

	localPath := strings.Split(filePath, string(filepath.Separator))
	sizeOfPath := len(localPath)
	url := fmt.Sprintf("https://files.joshrosso.com/img/posts/%s/%s", title, localPath[sizeOfPath-1])
	return fmt.Sprintf(ne.MdImagePattern, url, url), nil
}

func headerOverride(p *na.Page) string {
	var title string
	var description string
	var date string
	var images []string
	var aliases []string
	var o []byte
	separator := "---\n"
	o = append(o, separator...)

	// TODO(joshrosso): Do proper checks against these unsafe operations.
	title = p.Properties["Name"].(*na.TitleProperty).Title[0].PlainText

	if len(p.Properties["Description"].(*na.RichTextProperty).RichText) > 0 {
		description = p.Properties["Description"].(*na.RichTextProperty).RichText[0].PlainText
	}

	if len(p.Properties["Images"].(*na.RichTextProperty).RichText) > 0 {
		images = append(images, p.Properties["Images"].(*na.RichTextProperty).RichText[0].PlainText)
	}

	if p.Properties["Release"].(*na.DateProperty).Date != nil {
		date = p.Properties["Release"].(*na.DateProperty).Date.Start.String()
	}

	meta := headerMeta{
		title,
		description,
		date,
		images,
		aliases,
	}

	headerYaml, err := yaml.Marshal(meta)
	if err != nil {
		// TODO(joshrosso): need more eloquent error case.
		panic(err)
	}
	o = append(o, headerYaml...)
	o = append(o, separator...)
	o = append(o, "\n"...)
	o = append(o, "# "+title...)
	return string(o)
}

func RenderPage(id string, fileName string) {
	e, err := ne.NewExporter()
	if err != nil {
		fmt.Printf("Failed creating exporter attempting to render page %s\n", id)
		return
	}
	opts := ne.RenderOptions{
		ImageOpts: ne.ImageSaveOptions{},
		Overrides: ne.OverrideOptions{
			PageHeader: headerOverride,
			Image:      imageOverride,
		},
		SkipEmptyParagraphs: true,
	}
	out, err := e.Render(id, opts)
	if err != nil {
		fmt.Printf("Failed rendering page %s. Error: %s\n", id, err)
		return
	}
	err = os.WriteFile(fileName, out, 0666)
	if err != nil {
		fmt.Printf("Failed writing file %s for page %s. Error: %s\n", fileName, id, err)
		return
	}
}

// resolveNotionToken attempts to find a Notion integration token
// (https://developers.notion.com/docs/authorization). It will prefer a token
// set in the NOTION_TOKEN environment variable. If not present, it looks for
// this token in ${HOME}/.config/nexp.yaml. An error is returned when
// no token is found.
func resolveNotionToken() (string, error) {
	var t string
	t = os.Getenv(notionApiEnvVar)
	if t != "" {
		fmt.Println(t)
		return t, nil
	}

	conf, err := config.LoadNexpConfig()
	if err != nil {
		return t, err
	}
	if conf.Token == "" {
		return t, fmt.Errorf("Token retrieved from configuration was empty")
	}

	return conf.Token, nil
}

// resolveRenderConfig takes a set of RenderOptions and returns the first
// instance. This omits all subsequent instances that are passed.
func resolveRenderConfig(opts ...ne.RenderOptions) ne.RenderOptions {
	var config ne.RenderOptions

	if len(opts) < 1 {
		return config
	}

	return opts[0]
}
