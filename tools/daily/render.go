package main

import (
	"embed"
	"fmt"
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

func shanghai() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func buildView(cfg Config, date string, issueNo int, repos []Repo, c Copy) IssueView {
	t, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	for i := range repos {
		if strings.TrimSpace(repos[i].Language) == "" {
			repos[i].Language = "未知"
		}
	}
	total := 0
	langs := map[string]int{}
	for _, r := range repos {
		total += r.StarsToday
		langs[r.Language]++
	}
	var stats []LangStat
	for k, v := range langs {
		pct := 0
		if len(repos) > 0 {
			pct = (v*100 + len(repos)/2) / len(repos)
		}
		stats = append(stats, LangStat{Language: k, Count: v, Percent: pct})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count != stats[j].Count {
			return stats[i].Count > stats[j].Count
		}
		return stats[i].Language < stats[j].Language
	})
	top := make([]Repo, len(repos))
	copy(top, repos)
	sort.Slice(top, func(i, j int) bool { return top[i].StarsToday > top[j].StarsToday })
	if len(top) > 3 {
		top = top[:3]
	}
	view := IssueView{
		Date:       date,
		DateCN:     fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day()),
		IssueNo:    issueNo,
		Count:      len(repos),
		TotalStars: total,
		LangStats:  stats,
		TopThree:   top,
		SiteURL:    strings.TrimRight(cfg.SiteURL, "/"),
		WebURL:     strings.TrimRight(cfg.SiteURL, "/") + "/daily/" + date + "/",
		EditorNote: strings.TrimSpace(c.EditorNote),
		SourceNote: "本期由 CongoRAG Daily 自动编排。事实来自 GitHub Trending 页面与各仓库公开 README；摘要由 DeepSeek 生成，可能有误，请以仓库原文为准。",
	}
	if len(repos) > 0 {
		view.Lead = repos[0]
	}
	view.LeadBody = c.Lead.Body
	if len(view.LeadBody) == 0 {
		view.LeadBody = []string{repos[0].Description}
	}
	// copy 的 lead 标题/导语通过临时字段塞进 view：模板直接读 LeadCopy 更清楚。
	view.TrendTitle = c.Trend.Title
	view.TrendBody = c.Trend.Body
	if view.TrendTitle == "" {
		view.TrendTitle = "今天的榜单"
	}
	if len(view.TrendBody) == 0 {
		view.TrendBody = []string{"今天的榜单没有附加编辑观察。"}
	}
	byRepo := map[string]BriefCopy{}
	for _, b := range c.Briefs {
		byRepo[strings.ToLower(b.Repo)] = b
	}
	for _, r := range repos[1:] {
		key := strings.ToLower(r.Owner + "/" + r.Name)
		b, ok := byRepo[key]
		if !ok {
			b = BriefCopy{Repo: r.Owner + "/" + r.Name, Headline: r.Name, Body: r.Description}
		}
		if strings.TrimSpace(b.Headline) == "" {
			b.Headline = r.Name
		}
		if strings.TrimSpace(b.Body) == "" {
			b.Body = r.Description
		}
		if strings.TrimSpace(b.Body) == "" {
			b.Body = "README 没有提供描述。"
		}
		view.Briefs = append(view.Briefs, BriefView{Repo: r, Headline: b.Headline, Body: b.Body})
	}
	leadHeadline := c.Lead.Headline
	if leadHeadline == "" && len(repos) > 0 {
		leadHeadline = repos[0].Name
	}
	view.Subject = "CONGORAG DAILY · " + fmt.Sprintf("%d月%d日", int(t.Month()), t.Day()) + " · " + truncateRunes(leadHeadline, 34)
	view.Preheader = fmt.Sprintf("今天收录 %d 个仓库，合计新增 %s 颗星。", len(repos), comma(total))
	return view
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	if s != "" {
		out = append([]string{s}, out...)
	}
	return strings.Join(out, ",")
}

type renderData struct {
	Issue IssueView
	Lead  LeadCopy
	Copy  Copy
	Repos []Repo
}

func renderIssue(cfg Config, view IssueView, lead LeadCopy, c Copy, repos []Repo) (string, string, error) {
	data := renderData{Issue: view, Lead: lead, Copy: c, Repos: repos}
	funcs := htmltemplate.FuncMap{
		"comma": comma,
		"pct":   func(n int) string { return fmt.Sprintf("%d%%", n) },
		"first": func(s string) string {
			r := []rune(strings.TrimSpace(s))
			if len(r) == 0 {
				return ""
			}
			return string(r[0])
		},
		"dropFirst": func(s string) string {
			r := []rune(strings.TrimSpace(s))
			if len(r) <= 1 {
				return ""
			}
			return string(r[1:])
		},
	}
	htmlT, err := htmltemplate.New("issue.html.tmpl").Funcs(funcs).ParseFS(templateFS, "templates/issue.html.tmpl")
	if err != nil {
		return "", "", err
	}
	textT, err := texttemplate.New("issue.txt.tmpl").Funcs(texttemplate.FuncMap(funcs)).ParseFS(templateFS, "templates/issue.txt.tmpl")
	if err != nil {
		return "", "", err
	}
	var h, tx strings.Builder
	if err := htmlT.Execute(&h, data); err != nil {
		return "", "", err
	}
	if err := textT.Execute(&tx, data); err != nil {
		return "", "", err
	}
	return h.String(), tx.String(), nil
}

func writeIssue(cfg Config, date, html, text string) (string, error) {
	dir := cfg.OutDir
	if dir == "" {
		dir = "out"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	htmlPath := filepath.Join(dir, "issue-"+date+".html")
	textPath := filepath.Join(dir, "issue-"+date+".txt")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(textPath, []byte(text), 0o644); err != nil {
		return "", err
	}
	return htmlPath, nil
}
