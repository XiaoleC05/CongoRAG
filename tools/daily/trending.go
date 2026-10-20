package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var starsTodayRe = regexp.MustCompile(`([\d,]+)\s+stars today`)

const userAgent = "CongoRAG-Daily/1.0 (+https://oxelia51.com)"

func httpClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}

func fetchTrending(ctx context.Context) ([]Repo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/trending?since=daily", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github trending: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	var repos []Repo
	for _, a := range findArticles(root) {
		repo, ok := parseArticle(a)
		if !ok {
			continue
		}
		repo.Rank = len(repos) + 1
		repos = append(repos, repo)
	}
	if len(repos) < 5 {
		return nil, fmt.Errorf("github trending: only %d repositories parsed", len(repos))
	}
	return repos, nil
}

func findArticles(n *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "Box-row") {
					out = append(out, n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func parseArticle(a *html.Node) (Repo, bool) {
	h2 := findFirst(a, "h2")
	if h2 == nil {
		return Repo{}, false
	}
	link := findFirst(h2, "a")
	if link == nil {
		return Repo{}, false
	}
	href := attr(link, "href")
	parts := strings.Split(strings.Trim(href, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, false
	}
	repo := Repo{
		Owner: parts[0],
		Name:  parts[1],
		URL:   "https://github.com/" + parts[0] + "/" + parts[1],
	}
	// Description: first <p> after h2.
	if p := findFirstAfter(a, h2, "p"); p != nil {
		repo.Description = cleanText(textContent(p))
	}
	if repo.Description == "" {
		repo.Description = cleanText(textContent(h2))
	}
	if lang := findByAttr(a, "itemprop", "programmingLanguage"); lang != nil {
		repo.Language = cleanText(textContent(lang))
	}
	text := textContent(a)
	if m := starsTodayRe.FindStringSubmatch(text); len(m) == 2 {
		repo.StarsToday = atoiComma(m[1])
	}
	for _, x := range findAll(a, "a") {
		if strings.HasSuffix(attr(x, "href"), "/stargazers") {
			repo.StarsTotal = cleanText(textContent(x))
			break
		}
	}
	return repo, true
}

func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func atoiComma(s string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	return n
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func findFirst(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			return c
		}
		if found := findFirst(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func findFirstAfter(root, start *html.Node, tag string) *html.Node {
	found := false
	var out *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if out != nil {
			return
		}
		if n == start {
			found = true
		} else if found && n.Type == html.ElementNode && n.Data == tag {
			out = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func findAll(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == tag {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func findByAttr(n *html.Node, key, val string) *html.Node {
	var out *html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if out != nil {
			return
		}
		if x.Type == html.ElementNode {
			for _, a := range x.Attr {
				if a.Key == key && a.Val == val {
					out = x
					return
				}
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}
