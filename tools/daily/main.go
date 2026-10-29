package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("daily failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	dryRun := flag.Bool("dry-run", false, "render to files, do not send")
	force := flag.Bool("force", false, "send even if today's issue was already sent")
	dateFlag := flag.String("date", "", "issue date, YYYY-MM-DD (Asia/Shanghai)")
	fixture := flag.String("fixture", "", "render from a fixture JSON instead of the network")
	outDir := flag.String("out", "", "output directory for dry-run")
	webDir := flag.String("web-dir", "", "write the web archive under this directory")
	sendFixture := flag.Bool("send", false, "send even when -fixture is set")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *outDir != "" {
		cfg.OutDir = *outDir
	}
	if *webDir != "" {
		cfg.WebDir = *webDir
	}
	cfg.DryRun = cfg.DryRun || *dryRun
	cfg.Force = *force
	cfg.Date = *dateFlag
	cfg.Fixture = *fixture

	date := cfg.Date
	if date == "" {
		date = time.Now().In(shanghai()).Format("2006-01-02")
	}
	if _, err := time.ParseInLocation("2006-01-02", date, shanghai()); err != nil {
		return fmt.Errorf("invalid -date: %w", err)
	}

	state, err := loadState(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	issueNo := state.IssueNo
	if issueNo < 1 {
		issueNo = 1
	}

	var repos []Repo
	var c Copy
	summaryFailed := false
	if cfg.Fixture != "" {
		f, err := os.ReadFile(cfg.Fixture)
		if err != nil {
			return fmt.Errorf("read fixture: %w", err)
		}
		var fx Fixture
		if err := json.Unmarshal(f, &fx); err != nil {
			return fmt.Errorf("parse fixture: %w", err)
		}
		if fx.Date != "" {
			date = fx.Date
		}
		if fx.IssueNo > 0 {
			issueNo = fx.IssueNo
		}
		repos = fx.Repos
		c = fx.Copy
		if !*sendFixture {
			cfg.DryRun = true
		}
	} else {
		if !cfg.DryRun && !cfg.Force && state.LastDate == date {
			slog.Info("already sent today, skipping", "date", date)
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		repos, err = fetchTrending(ctx)
		if err != nil {
			return err
		}
		slog.Info("trending fetched", "count", len(repos))
		enrichReadmes(ctx, repos)
		c, err = summarize(ctx, cfg, repos)
		if err != nil {
			slog.Warn("summary failed, using fallback", "error", err)
			c = fallbackCopy(repos)
			summaryFailed = true
		} else if hits := checkCopy(c); len(hits) > 0 {
			slog.Warn("copy contains banned phrases", "hits", hits)
			if os.Getenv("DAILY_STRICT_COPY") == "1" {
				c = fallbackCopy(repos)
				summaryFailed = true
			}
		}
	}

	if len(repos) == 0 {
		return fmt.Errorf("no repositories")
	}
	if strings.TrimSpace(c.Lead.Headline) == "" {
		c = fallbackCopy(repos)
	}
	view := buildView(cfg, date, issueNo, repos, c)
	if cfg.Fixture != "" {
		view.SourceNote = "本期为版式样刊，摘要由编辑撰写；正式发行时由 DeepSeek 生成。事实来自 GitHub Trending 页面与各仓库公开 README，请以仓库原文为准。"
	} else if summaryFailed {
		view.SourceNote = "本期模型摘要失败，正文使用仓库原始描述。事实来自 GitHub Trending 页面与各仓库公开 README，请以仓库原文为准。"
	}
	html, text, err := renderIssue(cfg, view, c.Lead, c, repos)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	if cfg.DryRun {
		path, err := writeIssue(cfg, date, html, text)
		if err != nil {
			return fmt.Errorf("write issue: %w", err)
		}
		fmt.Println(path)
		fmt.Println(strings.TrimSuffix(path, ".html") + ".txt")
		fmt.Println("subject: " + view.Subject)
		return nil
	}

	if err := sendMail(cfg, view.Subject, text, html); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := writeWebArchive(cfg, date, html); err != nil {
		return fmt.Errorf("web archive: %w", err)
	}
	state.LastDate = date
	state.IssueNo = issueNo + 1
	if err := saveState(cfg.StateDir, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	slog.Info("sent", "date", date, "issue", issueNo, "to", cfg.MailTo)
	return nil
}

func enrichReadmes(ctx context.Context, repos []Repo) {
	client := httpClient()
	for i := range repos {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/readme", repos[i].Owner, repos[i].Name)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/vnd.github.raw")
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("readme fetch failed", "repo", repos[i].Owner+"/"+repos[i].Name, "error", err)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 12000))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			slog.Warn("readme unavailable", "repo", repos[i].Owner+"/"+repos[i].Name, "status", resp.Status)
			continue
		}
		repos[i].Readme = string(data)
	}
}
