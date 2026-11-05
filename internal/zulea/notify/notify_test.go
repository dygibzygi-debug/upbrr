// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secretToken = "TOKENSENTINEL123456789"

func TestDiscordAndTelegram(t *testing.T) {
	var got []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["path"] = r.URL.Path
		got = append(got, body)
		if strings.Contains(r.URL.Path, "fail") {
			http.Error(w, "no", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	long := strings.Repeat("x", 3000)
	errs := SendAll(context.Background(), []Channel{
		Discord{WebhookURL: "http://127.0.0.1" + strings.TrimPrefix(server.URL, "http://127.0.0.1") + "/api/webhooks/1/" + secretToken},
		Telegram{
			Token:   secretToken,
			ChatID:  "42",
			BaseURL: server.URL,
		},
	}, long)
	if errs["discord"] != nil || errs["telegram"] != nil || len(got) != 2 {
		t.Fatalf("errs %v got %d", errs, len(got))
	}
	content, _ := got[0]["content"].(string)
	if len([]rune(content)) != MaxText || got[1]["chat_id"] != "42" || got[1]["path"] != "/bot"+secretToken+"/sendMessage" {
		t.Fatalf("payloads %+v", got)
	}
	// Errors never expose the secret URL or token.
	err := Telegram{
		Token:   secretToken,
		ChatID:  "1",
		BaseURL: server.URL + "/fail",
	}.Send(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), secretToken) {
		t.Fatalf("error %v", err)
	}
	err = Discord{WebhookURL: "https://127.0.0.1:1/api/webhooks/1/" + secretToken}.Send(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), secretToken) {
		t.Fatalf("transport error must hide the webhook: %v", err)
	}
	if err := (Discord{WebhookURL: "ftp://x"}).Send(context.Background(), "x"); err == nil {
		t.Fatal("non-https webhook must be rejected")
	}
}
