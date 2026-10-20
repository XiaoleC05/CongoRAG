package main

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DeepSeekKey   string
	DeepSeekBase  string
	DeepSeekModel string

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	MailTo       string
	MailFromName string

	SiteURL  string
	StateDir string
	OutDir   string
	WebDir   string

	DryRun  bool
	Force   bool
	Date    string
	Fixture string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (Config, error) {
	port, err := strconv.Atoi(env("SMTP_PORT", "465"))
	if err != nil {
		return Config{}, fmt.Errorf("SMTP_PORT: %w", err)
	}
	return Config{
		DeepSeekKey:   os.Getenv("DEEPSEEK_API_KEY"),
		DeepSeekBase:  env("DEEPSEEK_BASE_URL", "https://api.deepseek.com/anthropic"),
		DeepSeekModel: env("DEEPSEEK_MODEL", "deepseek-flash"),
		SMTPHost:      env("SMTP_HOST", "smtp.qq.com"),
		SMTPPort:      port,
		SMTPUser:      os.Getenv("SMTP_USER"),
		SMTPPass:      os.Getenv("SMTP_PASS"),
		MailTo:        os.Getenv("MAIL_TO"),
		MailFromName:  env("MAIL_FROM_NAME", "CONGORAG DAILY"),
		SiteURL:       env("DAILY_SITE_URL", "https://oxelia51.com"),
		StateDir:      env("DAILY_STATE_DIR", "./state"),
		OutDir:        env("DAILY_OUT_DIR", "./out"),
		WebDir:        os.Getenv("DAILY_WEB_DIR"),
		DryRun:        os.Getenv("DAILY_DRY_RUN") == "1",
	}, nil
}
