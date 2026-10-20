package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func sendMail(cfg Config, subject, text, html string) error {
	if cfg.SMTPUser == "" || cfg.SMTPPass == "" {
		return fmt.Errorf("SMTP_USER / SMTP_PASS is empty")
	}
	if cfg.MailTo == "" {
		return fmt.Errorf("MAIL_TO is empty")
	}
	boundary := fmt.Sprintf("----congorag-%d", time.Now().UnixNano())
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.SetBoundary(boundary)

	tw, err := mw.CreatePart(map[string][]string{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(tw)
	if _, err := qp.Write([]byte(text)); err != nil {
		return err
	}
	if err := qp.Close(); err != nil {
		return err
	}

	hw, err := mw.CreatePart(map[string][]string{
		"Content-Type":              {"text/html; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp = quotedprintable.NewWriter(hw)
	if _, err := qp.Write([]byte(html)); err != nil {
		return err
	}
	if err := qp.Close(); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", cfg.MailFromName), cfg.SMTPUser)
	fmt.Fprintf(&msg, "To: %s\r\n", cfg.MailTo)
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&msg, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	fmt.Fprintf(&msg, "\r\n")
	msg.Write(body.Bytes())

	addr := net.JoinHostPort(cfg.SMTPHost, fmt.Sprintf("%d", cfg.SMTPPort))
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: cfg.SMTPHost,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("AUTH"); !ok {
		return fmt.Errorf("smtp server does not advertise AUTH")
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(cfg.SMTPUser); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	recipients := strings.Split(cfg.MailTo, ",")
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", r, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

type State struct {
	LastDate string `json:"last_date"`
	IssueNo  int    `json:"issue_no"`
}

func loadState(dir string) (State, error) {
	s := State{IssueNo: 1}
	p := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.IssueNo < 1 {
		s.IssueNo = 1
	}
	return s, nil
}

func saveState(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), append(b, '\n'), 0o644)
}

func writeWebArchive(cfg Config, date, html string) error {
	if cfg.WebDir == "" {
		return nil
	}
	dir := filepath.Join(cfg.WebDir, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644)
}
