// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package notify sends short status summaries to Discord (webhook) and
// Telegram (bot). Webhook URLs and bot tokens are secrets: they are read from
// private files by the caller and never appear in errors or logs.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxText bounds a message (Discord allows 2000 characters per message).
const MaxText = 1900

// Channel delivers one message.
type Channel interface {
	Name() string
	Send(ctx context.Context, text string) error
}

func trim(text string) string {
	if r := []rune(text); len(r) > MaxText {
		return string(r[:MaxText-1]) + "…"
	}
	return text
}

func post(ctx context.Context, client *http.Client, url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return errors.New("encode failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return errors.New("build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The transport error embeds the secret URL; report only the class.
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Discord posts to a webhook URL.
type Discord struct {
	WebhookURL string
	HTTP       *http.Client
}

// Name implements Channel.
func (Discord) Name() string { return "discord" }

// Send implements Channel.
func (d Discord) Send(ctx context.Context, text string) error {
	if !strings.HasPrefix(d.WebhookURL, "https://") && !strings.HasPrefix(d.WebhookURL, "http://127.0.0.1") {
		return errors.New("discord: webhook must be an https URL")
	}
	if err := post(ctx, d.HTTP, d.WebhookURL, map[string]any{
		"content":          trim(text),
		"allowed_mentions": map[string]any{"parse": []string{}},
	}); err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	return nil
}

// Telegram sends through the Bot API.
type Telegram struct {
	Token  string
	ChatID string
	// BaseURL overrides https://api.telegram.org (tests).
	BaseURL string
	HTTP    *http.Client
}

// Name implements Channel.
func (Telegram) Name() string { return "telegram" }

// Send implements Channel.
func (t Telegram) Send(ctx context.Context, text string) error {
	if t.Token == "" || t.ChatID == "" {
		return errors.New("telegram: token and chat id are required")
	}
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	if err := post(ctx, t.HTTP, strings.TrimRight(base, "/")+"/bot"+t.Token+"/sendMessage", map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     trim(text),
		"disable_web_page_preview": true,
	}); err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	return nil
}

// SendAll delivers text to every channel and returns per-channel errors.
func SendAll(ctx context.Context, channels []Channel, text string) map[string]error {
	out := map[string]error{}
	for _, ch := range channels {
		out[ch.Name()] = ch.Send(ctx, text)
	}
	return out
}
