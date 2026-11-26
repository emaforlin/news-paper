package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/epub"
	"github.com/emaforlin/news-paper/internal/mail"
)

const (
	epubType = "application/epub+zip"
	// bodyTitleRunes keeps every body line under SMTP's 998-octet limit,
	// which mail.Build enforces: a huge headline must not block the edition.
	bodyTitleRunes = 150
)

// Delivery is one EPUB to email.
type Delivery struct {
	Subject, Body, Filename string
	EPUB                    []byte
	Date                    time.Time
}

// BuildMessage renders the email from cfg.SMTP's addresses.
func BuildMessage(cfg *config.Config, dl Delivery) ([]byte, error) {
	return mail.Build(mail.Message{
		From:    cfg.SMTP.From,
		To:      cfg.SMTP.To,
		Subject: dl.Subject,
		Body:    dl.Body,
		Attach:  mail.Attachment{Filename: dl.Filename, ContentType: epubType, Data: dl.EPUB},
		Date:    dl.Date,
	})
}

// SMTPConfig maps the app config to the transport's.
func SMTPConfig(cfg *config.Config) mail.SMTPConfig {
	return mail.SMTPConfig{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		TLSMode:  cfg.SMTP.TLSMode,
		Username: cfg.SMTP.Username,
		Password: string(cfg.SMTP.Password),
	}
}

// body is the plain-text table of contents.
func body(ed epub.Edition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", ed.Title, epub.LongDate(ed.Date))
	for i, a := range ed.Articles {
		title := []rune(strings.Join(strings.Fields(a.Title), " "))
		if len(title) > bodyTitleRunes {
			title = append(title[:bodyTitleRunes-1], '…')
		}
		fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, string(title), a.Domain())
	}
	return b.String()
}
