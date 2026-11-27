package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"milton_prism/pkg/log"
)

// buildTextMessage renders a plain-text UTF-8 email with CRLF line ends. Any
// CR or LF in a header value is refused, so no field can add headers.
func buildTextMessage(fromName, fromAddr, to, subject, body string) (string, error) {
	for _, v := range []string{fromName, fromAddr, to, subject} {
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("mailer: line break in a header value")
		}
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := "localhost"
	if at := strings.LastIndex(fromAddr, "@"); at >= 0 && at < len(fromAddr)-1 {
		domain = fromAddr[at+1:]
	}
	from := (&mail.Address{Name: fromName, Address: fromAddr}).String()

	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	_, _ = w.Write([]byte(strings.ReplaceAll(body, "\r\n", "\n")))
	_ = w.Close()

	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="UTF-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(qp.String(), "\r\n", "\n"), "\n", "\r\n"))
	return b.String(), nil
}

// SendText for smtpMailer: same rate limit, retries and timeout as Send.
func (m *smtpMailer) SendText(recipient, subject, body string) error {
	msg, err := buildTextMessage(m.cfg.DefaultSenderName, m.cfg.DefaultSenderAddr, recipient, subject, body)
	if err != nil {
		return err
	}
	return m.deliver(recipient, msg)
}

// SendText for logMailer: it records that a message was dropped, never its body.
// The body carries the link, and the link carries the token.
func (m *logMailer) SendText(recipient, subject, _ string) error {
	log.Infof("[logMailer] email dropped (dry run): subject=%q", subject)
	return nil
}

func (m *redirectMailer) SendText(recipient, subject, body string) error {
	log.Infof("Redirecting email originally for %s to %s", recipient, m.redirectAddress)
	return m.wrappedMailer.SendText(m.redirectAddress, "[for "+recipient+"] "+subject, body)
}

// FileSinkMailer writes each message to a file instead of sending it. It exists
// for local development, where there is no SMTP server: open the file, follow the
// link. The files hold live tokens, so the mode is 0600 and the directory 0700.
// Never enable it outside a developer's machine.
type FileSinkMailer struct{ dir string }

// NewFileSinkMailer creates the directory if needed.
func NewFileSinkMailer(dir string) (*FileSinkMailer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mailer: create sink dir: %w", err)
	}
	return &FileSinkMailer{dir: dir}, nil
}

// Send is not supported: the sink serves the account emails, which use SendText.
func (s *FileSinkMailer) Send(string, string, TemplateData, ...string) error {
	return fmt.Errorf("mailer: the file sink only supports SendText")
}

func (s *FileSinkMailer) SendText(recipient, subject, body string) error {
	if strings.ContainsAny(recipient+subject, "\r\n") {
		return fmt.Errorf("mailer: line break in a header value")
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	name := fmt.Sprintf("%s-%s-%s.txt", time.Now().UTC().Format("20060102T150405.000"), safeFilePart(recipient), hex.EncodeToString(id[:]))
	content := "To: " + recipient + "\nSubject: " + subject + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o600); err != nil {
		return fmt.Errorf("mailer: write sink file: %w", err)
	}
	return nil
}

// safeFilePart keeps letters, digits, '@', '.' and '-' so a recipient cannot
// choose a path.
func safeFilePart(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '@', r == '-':
			return r
		}
		return '_'
	}, s)
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
