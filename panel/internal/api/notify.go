package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Message is one notification, rendered per channel type.
type Message struct {
	Title     string  `json:"title"`
	Firing    bool    `json:"firing"`
	Rule      string  `json:"rule"`
	Server    string  `json:"server"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Test      bool    `json:"test,omitempty"`
}

// channelFields lists each type's config keys; secret ones are never sent
// back to the browser.
var channelFields = map[string]struct{ all, secret []string }{
	"email":    {[]string{"host", "port", "username", "password", "from", "to", "tls"}, []string{"password"}},
	"telegram": {[]string{"bot_token", "chat_id"}, []string{"bot_token"}},
	"discord":  {[]string{"webhook_url"}, []string{"webhook_url"}},
	"webhook":  {[]string{"url", "secret"}, []string{"secret"}},
}

const secretMask = "••••••••"

var httpClient = &http.Client{Timeout: 10 * time.Second}

func (a *API) channelConfig(c *store.Channel) (map[string]string, error) {
	plain, err := a.open(c.Config)
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	return cfg, json.Unmarshal(plain, &cfg)
}

func validateChannel(typ string, cfg map[string]string) error {
	need := func(keys ...string) error {
		for _, k := range keys {
			if strings.TrimSpace(cfg[k]) == "" {
				return fmt.Errorf("%s is required", k)
			}
		}
		return nil
	}
	switch typ {
	case "email":
		if err := need("host", "port", "from", "to"); err != nil {
			return err
		}
		if p, err := strconv.Atoi(cfg["port"]); err != nil || p < 1 || p > 65535 {
			return errors.New("port must be 1-65535")
		}
		if _, err := mail.ParseAddress(cfg["from"]); err != nil {
			return errors.New("from is not a valid email address")
		}
		for _, to := range strings.Split(cfg["to"], ",") {
			if _, err := mail.ParseAddress(strings.TrimSpace(to)); err != nil {
				return fmt.Errorf("%q is not a valid email address", strings.TrimSpace(to))
			}
		}
		switch cfg["tls"] {
		case "", "starttls", "tls", "none":
		default:
			return errors.New("tls must be starttls, tls or none")
		}
	case "telegram":
		if err := need("bot_token", "chat_id"); err != nil {
			return err
		}
		if !regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{20,}$`).MatchString(cfg["bot_token"]) {
			return errors.New("bot token looks wrong (expected 123456:ABC…)")
		}
		if !regexp.MustCompile(`^(-?\d+|@[A-Za-z0-9_]{5,})$`).MatchString(cfg["chat_id"]) {
			return errors.New("chat id must be a number or @channelname")
		}
	case "discord":
		if err := need("webhook_url"); err != nil {
			return err
		}
		u, err := url.Parse(cfg["webhook_url"])
		if err != nil || u.Scheme != "https" || (u.Host != "discord.com" && u.Host != "discordapp.com") || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return errors.New("must be a https://discord.com/api/webhooks/… URL")
		}
	case "webhook":
		if err := need("url"); err != nil {
			return err
		}
		u, err := url.Parse(cfg["url"])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("url must be http(s)://…")
		}
	default:
		return errors.New("type must be email, telegram, discord or webhook")
	}
	return nil
}

func send(ctx context.Context, typ string, cfg map[string]string, m Message) error {
	switch typ {
	case "telegram":
		body, _ := json.Marshal(map[string]any{"chat_id": cfg["chat_id"], "text": m.Title, "disable_web_page_preview": true})
		return postJSON(ctx, "https://api.telegram.org/bot"+cfg["bot_token"]+"/sendMessage", body, nil)
	case "discord":
		color := 0xE5484D
		if !m.Firing {
			color = 0x30A46C
		}
		body, _ := json.Marshal(map[string]any{
			"username": "servermgr",
			"embeds":   []map[string]any{{"title": m.Title, "color": color, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		})
		return postJSON(ctx, cfg["webhook_url"], body, nil)
	case "webhook":
		body, _ := json.Marshal(map[string]any{"event": "alert", "sent_at": time.Now().UTC(), "alert": m})
		hdr := map[string]string{}
		if s := cfg["secret"]; s != "" {
			mac := hmac.New(sha256.New, []byte(s))
			mac.Write(body)
			hdr["X-Servermgr-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		return postJSON(ctx, cfg["url"], body, hdr)
	case "email":
		return sendMail(cfg, m)
	}
	return errors.New("unknown channel type")
}

func postJSON(ctx context.Context, u string, body []byte, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "servermgr")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Never echo the URL back: for Telegram/Discord it contains the secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("request failed: %v", ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

func sendMail(cfg map[string]string, m Message) error {
	addr := net.JoinHostPort(cfg["host"], cfg["port"])
	from, _ := mail.ParseAddress(cfg["from"])
	var to []string
	for _, t := range strings.Split(cfg["to"], ",") {
		if a, err := mail.ParseAddress(strings.TrimSpace(t)); err == nil {
			to = append(to, a.Address)
		}
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Title)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n\r\nSent by servermgr.\r\n",
		from.String(), strings.Join(to, ", "), subject, time.Now().Format(time.RFC1123Z), subject)

	tlsCfg := &tls.Config{ServerName: cfg["host"], MinVersion: tls.VersionTLS12}
	var c *smtp.Client
	var err error
	mode := cfg["tls"]
	if mode == "" {
		mode = "starttls"
	}
	if mode == "tls" {
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, tlsCfg)
		if derr != nil {
			return derr
		}
		c, err = smtp.NewClient(conn, cfg["host"])
	} else {
		conn, derr := net.DialTimeout("tcp", addr, 10*time.Second)
		if derr != nil {
			return derr
		}
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		c, err = smtp.NewClient(conn, cfg["host"])
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS (choose TLS or none)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if cfg["username"] != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg["username"], cfg["password"], cfg["host"])); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// notify delivers m to the given channels in the background.
func (a *API) notify(channelIDs []string, m Message) {
	for _, id := range channelIDs {
		go func(id string) {
			ctx, cancel := context.WithTimeout(a.baseCtx(), 30*time.Second)
			defer cancel()
			c, err := a.Store.GetChannel(ctx, id)
			if err != nil || !c.Enabled {
				return
			}
			cfg, err := a.channelConfig(c)
			if err == nil {
				err = send(ctx, c.Type, cfg, m)
			}
			msg := ""
			if err != nil {
				msg = err.Error()
				a.Log.Warn("notification failed", "channel", c.Name, "err", err)
			}
			_ = a.Store.ChannelResult(ctx, c.ID, msg)
		}(id)
	}
}

// ---- handlers ----

type channelView struct {
	*store.Channel
	Config map[string]string `json:"config"`
}

func (a *API) viewChannel(c *store.Channel) channelView {
	cfg, err := a.channelConfig(c)
	if err != nil {
		cfg = map[string]string{}
	}
	for _, k := range channelFields[c.Type].secret {
		if cfg[k] != "" {
			cfg[k] = secretMask
		}
	}
	return channelView{Channel: c, Config: cfg}
}

func (a *API) handleListChannels(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListChannels(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]channelView, 0, len(list))
	for _, c := range list {
		out = append(out, a.viewChannel(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSaveChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		Config  map[string]string `json:"config"`
		Enabled bool              `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	}
	fields, ok := channelFields[req.Type]
	if !ok {
		writeErr(w, http.StatusBadRequest, "type must be email, telegram, discord or webhook")
		return
	}
	cfg := map[string]string{}
	for k, v := range req.Config {
		if !slices.Contains(fields.all, k) || len(v) > 2048 {
			writeErr(w, http.StatusBadRequest, "unknown or oversized field "+k)
			return
		}
		cfg[k] = strings.TrimSpace(v)
	}
	c := &store.Channel{ID: r.PathValue("id"), Name: req.Name, Type: req.Type, Enabled: req.Enabled}
	// A masked (or empty) secret on update means "keep the stored one".
	if c.ID != "" {
		cur, err := a.Store.GetChannel(r.Context(), c.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		if old, err := a.channelConfig(cur); err == nil && cur.Type == req.Type {
			for _, k := range fields.secret {
				if cfg[k] == secretMask || cfg[k] == "" {
					cfg[k] = old[k]
				}
			}
		}
	}
	for _, k := range fields.secret {
		if cfg[k] == secretMask {
			writeErr(w, http.StatusBadRequest, k+" is required")
			return
		}
	}
	if err := validateChannel(req.Type, cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	plain, _ := json.Marshal(cfg)
	sealed, err := a.seal(plain)
	if err != nil {
		a.internal(w, err)
		return
	}
	c.Config = sealed
	if err := a.Store.SaveChannel(r.Context(), c); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "channel.save", c.Name, map[string]string{"type": c.Type}, true)
	writeJSON(w, http.StatusOK, a.viewChannel(c))
}

func (a *API) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if err := a.Store.DeleteChannel(r.Context(), c.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "channel.delete", c.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestChannel sends a test message synchronously and reports the result.
func (a *API) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	cfg, err := a.channelConfig(c)
	if err != nil {
		a.internal(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	m := Message{Title: "[TEST] servermgr notification test from " + whoFrom(r).Actor, Firing: true, Test: true}
	err = send(ctx, c.Type, cfg, m)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = a.Store.ChannelResult(r.Context(), c.ID, msg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "sent"})
}
