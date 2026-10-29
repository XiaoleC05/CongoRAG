// Package recommend 把 arXiv 的候选论文按"与本地知识库的相关性"排序（参赛版 M4）。
//
// 【为什么在服务端做排序而不是直接把 arXiv 结果给前端】推荐要满足三件事：
// 与本库主题相关、尽量新、优先带开源代码。这三件事都可以从标题/摘要/comment
// 里算出来，不需要模型调用；纯函数实现也便于单测和解释"为什么推荐它"。
package recommend

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/XiaoleC05/CongoRAG/internal/arxiv"
)

// Result 是一条推荐：论文本身 + 分数 + 推荐理由 + 代码链接。
type Result struct {
	Paper     arxiv.Paper `json:"paper"`
	Score     float64     `json:"score"`
	Reason    string      `json:"reason"`
	GithubURL string      `json:"githubUrl"`
}

var githubPattern = regexp.MustCompile(`https?://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)

var stopWords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "this": {}, "that": {},
	"are": {}, "was": {}, "were": {}, "using": {}, "based": {}, "into": {}, "towards": {},
	"toward": {}, "via": {}, "our": {}, "can": {}, "its": {}, "their": {}, "between": {},
	"learning": {}, "paper": {}, "model": {}, "models": {}, "method": {}, "methods": {},
}

// Build 对候选论文打分排序，返回最多 limit 条。
//
// 【排序规则】相关性（与库内标题的词重合数）为主，近期发表与 GitHub 代码
// 为加分项；同一篇论文（标题归一化后相同）只保留一条。
// votes 里 down 过的论文直接过滤（用户明确表示不感兴趣）。
func Build(libraryTitles []string, papers []arxiv.Paper, votes map[string]string, limit int) []Result {
	if limit <= 0 {
		limit = 10
	}
	titles := make([]string, 0, len(libraryTitles))
	for _, t := range libraryTitles {
		if norm := normalizeTitle(t); norm != "" {
			titles = append(titles, norm)
		}
	}
	libraryTokens := map[string]struct{}{}
	for _, t := range titles {
		for _, tok := range tokens(t) {
			libraryTokens[tok] = struct{}{}
		}
	}

	now := time.Now()
	seenTitle := map[string]struct{}{}
	out := make([]Result, 0, len(papers))
	for _, p := range papers {
		if votes[p.ID] == "down" {
			continue
		}
		norm := normalizeTitle(p.Title)
		if norm == "" {
			continue
		}
		if _, dup := seenTitle[norm]; dup {
			continue
		}
		seenTitle[norm] = struct{}{}
		// 已在库里的同一篇论文不重复推荐。
		already := false
		for _, t := range titles {
			if t == norm {
				already = true
				break
			}
		}
		if already {
			continue
		}

		matched := make([]string, 0, 6)
		for _, tok := range tokens(p.Title + " " + p.Abstract) {
			if _, ok := libraryTokens[tok]; ok {
				matched = append(matched, tok)
			}
		}
		// 去重关键词并限制展示数量：理由要短。
		matched = uniqueLimited(matched, 4)
		score := float64(len(matched))
		if len(matched) == 0 {
			continue
		}

		reasonParts := make([]string, 0, 3)
		if best := bestLibraryTitle(titles, matched); best != "" {
			reasonParts = append(reasonParts, "与库内《"+best+"》主题相近")
		} else {
			reasonParts = append(reasonParts, "与本库关键词重合")
		}
		reasonParts = append(reasonParts, "命中关键词："+strings.Join(matched, " / "))
		if t, err := time.Parse(time.RFC3339, p.Published); err == nil && now.Sub(t) < 90*24*time.Hour {
			score += 0.5
			reasonParts = append(reasonParts, "近 90 天发表")
		}
		github := findGithub(p.Abstract + " " + p.Comment)
		if github != "" {
			score += 0.8
			reasonParts = append(reasonParts, "附开源代码")
		}
		out = append(out, Result{
			Paper:     p,
			Score:     score,
			Reason:    strings.Join(reasonParts, "；"),
			GithubURL: github,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Paper.Published > out[j].Paper.Published
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// QueryForTitles 用库内标题里出现频率最高的词拼一个 arXiv 查询。
//
// 用 all: 前缀做全字段检索（标题+摘要+作者），一条查询最多 4 个词——
// arXiv API 对长查询很敏感，词越多越容易返回空。
func QueryForTitles(titles []string) string {
	freq := map[string]int{}
	order := map[string]int{}
	for _, t := range titles {
		for _, tok := range tokens(t) {
			if _, stop := stopWords[tok]; stop {
				continue
			}
			if len(tok) < 4 {
				continue
			}
			if _, ok := order[tok]; !ok {
				order[tok] = len(order)
			}
			freq[tok]++
		}
	}
	type kw struct {
		word  string
		count int
		order int
	}
	kws := make([]kw, 0, len(freq))
	for w, c := range freq {
		kws = append(kws, kw{word: w, count: c, order: order[w]})
	}
	sort.Slice(kws, func(i, j int) bool {
		if kws[i].count != kws[j].count {
			return kws[i].count > kws[j].count
		}
		return kws[i].order < kws[j].order
	})
	if len(kws) > 4 {
		kws = kws[:4]
	}
	if len(kws) == 0 {
		return "all:machine learning"
	}
	parts := make([]string, 0, len(kws))
	for _, k := range kws {
		parts = append(parts, "all:"+k.word)
	}
	return strings.Join(parts, " OR ")
}

var nonWordPattern = regexp.MustCompile(`[^a-z0-9]+`)

func tokens(s string) []string {
	s = strings.ToLower(s)
	s = nonWordPattern.ReplaceAllString(s, " ")
	return strings.Fields(s)
}

func normalizeTitle(s string) string {
	return strings.Join(tokens(s), " ")
}

func uniqueLimited(in []string, limit int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func bestLibraryTitle(titles []string, matched []string) string {
	best, bestCount := "", 0
	for _, t := range titles {
		count := 0
		for _, m := range matched {
			if strings.Contains(t, m) {
				count++
			}
		}
		if count > bestCount {
			best, bestCount = t, count
		}
	}
	return best
}

func findGithub(text string) string {
	return githubPattern.FindString(text)
}
