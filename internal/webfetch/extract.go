package webfetch

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

func ExtractText(htmlBytes []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	var sb strings.Builder
	skipTags := map[string]bool{
		"script":   true,
		"style":    true,
		"noscript": true,
		"head":     true,
	}
	blockTags := map[string]bool{
		"p":       true,
		"div":     true,
		"h1":      true,
		"h2":      true,
		"h3":      true,
		"h4":      true,
		"h5":      true,
		"h6":      true,
		"li":      true,
		"br":      true,
		"tr":      true,
		"table":   true,
		"section": true,
		"article": true,
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
			return
		}
		isBlock := n.Type == html.ElementNode && blockTags[n.Data]
		if isBlock {
			sb.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if isBlock {
			sb.WriteString("\n")
		}
	}

	walk(doc)

	text := sb.String()
	whitespaceRe := regexp.MustCompile(`[ \t]+`)
	text = whitespaceRe.ReplaceAllString(text, " ")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	text = strings.Join(lines, "\n")
	newlineRe := regexp.MustCompile(`\n{3,}`)
	text = newlineRe.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)

	return text, nil
}
