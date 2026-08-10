package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Settings struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // write-only on GET (masked)
	From     string `json:"from"`
	UseTLS   bool   `json:"useTLS"`
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	st := Settings{Port: 587, UseTLS: true}
	rows, err := s.db.Query(ctx, `select key, value from app_settings where key like 'mail_%'`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		switch k {
		case "mail_enabled":
			st.Enabled = v == "1" || strings.EqualFold(v, "true")
		case "mail_host":
			st.Host = v
		case "mail_port":
			if p, err := strconv.Atoi(v); err == nil {
				st.Port = p
			}
		case "mail_username":
			st.Username = v
		case "mail_password":
			if v != "" {
				st.Password = "********"
			}
		case "mail_from":
			st.From = v
		case "mail_use_tls":
			st.UseTLS = v != "0" && !strings.EqualFold(v, "false")
		}
	}
	return st, rows.Err()
}

func (s *Service) Save(ctx context.Context, in Settings) error {
	if in.Port <= 0 {
		in.Port = 587
	}
	pairs := map[string]string{
		"mail_enabled":  boolStr(in.Enabled),
		"mail_host":     strings.TrimSpace(in.Host),
		"mail_port":     strconv.Itoa(in.Port),
		"mail_username": strings.TrimSpace(in.Username),
		"mail_from":     strings.TrimSpace(in.From),
		"mail_use_tls":  boolStr(in.UseTLS),
	}
	if in.Password != "" && in.Password != "********" {
		pairs["mail_password"] = in.Password
	}
	for k, v := range pairs {
		if _, err := s.db.Exec(ctx, `
			insert into app_settings (key, value, updated_at) values ($1, $2, now())
			on conflict (key) do update set value = excluded.value, updated_at = now()`, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) rawPassword(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRow(ctx, `select value from app_settings where key = 'mail_password'`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Service) Send(ctx context.Context, to, subject, body string) error {
	st, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if !st.Enabled {
		return errors.New("mail disabled")
	}
	if st.Host == "" || st.From == "" || to == "" {
		return errors.New("mail settings incomplete")
	}
	pass, err := s.rawPassword(ctx)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", st.Host, st.Port)
	msg := []byte("From: " + st.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" + body + "\r\n")

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	var client *smtp.Client
	if st.UseTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: st.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		client, err = smtp.NewClient(tlsConn, st.Host)
	} else {
		client, err = smtp.NewClient(conn, st.Host)
	}
	if err != nil {
		return err
	}
	defer client.Close()

	if st.Username != "" {
		auth := smtp.PlainAuth("", st.Username, pass, st.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(st.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func boolStr(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
