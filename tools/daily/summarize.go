package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const systemPrompt = `你是一家中文科技报纸的编辑，负责把当天的 GitHub Trending 写成一份日报。

写作规则：
- 只使用用户提供的 FACTS 和 README 摘录。不得补充未给出的功能、性能数字、融资、团队、用户数、客户、路线图。
- 不要写“为什么上榜”的因果。GitHub 不公布原因。星数、排名、总星数由版面元数据展示，标题和正文都不要重复这些数字。
- 不要使用这些词：赋能、助力、打造、一站式、全方位、无缝、极致、强大、智能、颠覆、重塑、革命性、业界领先、生产级、企业级、开箱即用、轻松上手、深受信赖、值得注意的是、总而言之、综上所述。
- 不要用“首先/其次/最后”“不仅……而且……”“从……到……”这类机械结构。
- 标题 10-20 个汉字，具体，说清项目做的事；不要把星数、排名、“今日”写进标题。不要用“引爆”“王者”“终局”这类词。
- 导语 1-2 句。正文 2-3 段，每段 2-4 句。句子长短交替，不要每段一样长。
- README 只有一句话时，就写一句话，不要扩写成三段。
- 每条 brief 2-3 句：第一句说它是什么或解决什么，第二句说一个具体细节或限制。不要每条都用“把……做成……”或“给……套一层……”的句式。
- 不要写作者个人处境、求职状态、融资、商业计划、社群迁移、招聘信息；这些和读者使用工具无关。
- 不要用“它”开头指代项目；用项目名或“这个项目”。
- 每条 brief 只写对应的仓库，不要跨仓库编故事。
- trend 写两段，至少引用三个具体仓库；先给判断，再给依据。它是编辑观察，不是统计，不要编造数字。
- editor_note 两到三句，不要重复 trend 的内容。

只返回 JSON，不要 markdown 代码块。字段：
{
  "lead": {"headline": "string", "dek": "string", "body": ["string", "string"]},
  "briefs": [{"repo": "owner/name", "headline": "string", "body": "string"}],
  "trend": {"title": "string", "body": ["string"]},
  "editor_note": "string"
}

briefs 要覆盖 FACTS 里除第 1 名以外的每个仓库，顺序与 FACTS 一致。`

type anthReq struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      string    `json:"system"`
	Messages    []anthMsg `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
}

type anthMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func summarize(ctx context.Context, cfg Config, repos []Repo) (Copy, error) {
	if cfg.DeepSeekKey == "" {
		return Copy{}, errors.New("DEEPSEEK_API_KEY is empty")
	}
	facts := make([]Repo, len(repos))
	copy(facts, repos)
	for i := range facts {
		if len(facts[i].Readme) > 4000 {
			facts[i].Readme = facts[i].Readme[:4000] + "\n[truncated]"
		}
	}
	total := 0
	langs := map[string]int{}
	for _, r := range repos {
		total += r.StarsToday
		langs[r.Language]++
	}
	var langParts []string
	for k, v := range langs {
		langParts = append(langParts, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(langParts)
	factsJSON, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return Copy{}, err
	}
	user := fmt.Sprintf("日期：%s\n今日收录：%d 个仓库\n今日新增星数合计：%d\n语言分布：%s\n\nFACTS：\n%s",
		time.Now().In(shanghai()).Format("2006-01-02"), len(repos), total, strings.Join(langParts, ", "), factsJSON)

	raw, err := callAnthropic(ctx, cfg, systemPrompt, user)
	if err != nil {
		return Copy{}, err
	}
	raw = stripCodeFence(raw)
	if os.Getenv("DAILY_DEBUG_RAW") == "1" {
		_ = os.WriteFile("raw-summary.txt", []byte(raw), 0o644)
	}
	var c Copy
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
			if err2 := json.Unmarshal([]byte(raw[i:j+1]), &c); err2 != nil {
				return Copy{}, fmt.Errorf("decode summary: %w", err)
			}
		} else {
			return Copy{}, fmt.Errorf("decode summary: %w", err)
		}
	}
	if strings.TrimSpace(c.Lead.Headline) == "" {
		return Copy{}, errors.New("summary has empty lead headline")
	}
	return c, nil
}

func callAnthropic(ctx context.Context, cfg Config, system, user string) (string, error) {
	body := anthReq{
		Model:       cfg.DeepSeekModel,
		MaxTokens:   8192,
		System:      system,
		Messages:    []anthMsg{{Role: "user", Content: user}},
		Temperature: 0.35,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(cfg.DeepSeekBase, "/") + "/v1/messages"
	client := &http.Client{Timeout: 180 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", cfg.DeepSeekKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("user-agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("anthropic: %s: %s", resp.Status, string(data))
			time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("anthropic: %s: %s", resp.Status, string(data))
		}
		var ar anthResp
		if err := json.Unmarshal(data, &ar); err != nil {
			return "", fmt.Errorf("decode anthropic response: %w", err)
		}
		if ar.Error != nil {
			return "", fmt.Errorf("anthropic: %s", ar.Error.Message)
		}
		var b strings.Builder
		for _, c := range ar.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		if strings.TrimSpace(b.String()) == "" {
			return "", errors.New("anthropic: empty content")
		}
		if ar.StopReason == "max_tokens" {
			return "", fmt.Errorf("anthropic: summary truncated at max_tokens (%d)", 8192)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("anthropic: %w", lastErr)
}

var bannedCopyWords = []string{
	"赋能", "助力", "打造", "一站式", "全方位", "无缝", "极致",
	"颠覆", "重塑", "革命性", "业界领先", "生产级", "企业级",
	"开箱即用", "轻松上手", "深受信赖", "值得注意的是", "总而言之",
	"综上所述", "未来可期", "让我们一起", "开启新篇章",
}

func checkCopy(c Copy) []string {
	var texts []string
	texts = append(texts, c.Lead.Headline, c.Lead.Dek)
	texts = append(texts, c.Lead.Body...)
	for _, b := range c.Briefs {
		texts = append(texts, b.Headline, b.Body)
	}
	texts = append(texts, c.Trend.Title)
	texts = append(texts, c.Trend.Body...)
	texts = append(texts, c.EditorNote)
	var hits []string
	seen := map[string]bool{}
	for _, t := range texts {
		for _, w := range bannedCopyWords {
			if strings.Contains(t, w) && !seen[w] {
				seen[w] = true
				hits = append(hits, w)
			}
		}
	}
	return hits
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func fallbackCopy(repos []Repo) Copy {
	if len(repos) == 0 {
		return Copy{}
	}
	lead := repos[0]
	c := Copy{
		Lead: LeadCopy{
			Headline: fmt.Sprintf("%s 今天排在日榜第一", lead.Name),
			Dek:      lead.Description,
			Body: []string{
				fmt.Sprintf("%s 是今天 GitHub Trending 日榜的第 1 名。%s", lead.Owner+"/"+lead.Name, lead.Description),
				fmt.Sprintf("今天新增 %d 颗星，总星数 %s，主要语言 %s。", lead.StarsToday, lead.StarsTotal, lead.Language),
			},
		},
		Trend: TrendCopy{
			Title: "今天的榜单",
			Body:  []string{"这份日报在抓取时没有拿到模型摘要，下面保留的是各仓库的原始描述。数字来自 GitHub Trending 页面。"},
		},
	}
	for _, r := range repos[1:] {
		c.Briefs = append(c.Briefs, BriefCopy{
			Repo:     r.Owner + "/" + r.Name,
			Headline: r.Name,
			Body:     r.Description,
		})
	}
	return c
}
