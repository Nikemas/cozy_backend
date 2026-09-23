package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// StaffMessenger delivers a text message to the staff channel (Telegram
// chat today). text is Telegram-HTML (<b>, <a href>, escaped entities).
type StaffMessenger interface {
	SendStaffMessage(ctx context.Context, text string) error
}

// NopStaffMessenger is used when TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID are
// unset: it only logs.
type NopStaffMessenger struct{}

func (NopStaffMessenger) SendStaffMessage(_ context.Context, text string) error {
	slog.Info("notify: Telegram not configured, staff message not sent", "chars", len(text))
	return nil
}

// TelegramClient posts to one chat through the Telegram Bot API
// (sendMessage). Create the bot via @BotFather, add it to the staff
// group, and use that group's chat id (negative number for groups).
type TelegramClient struct {
	httpClient *http.Client
	baseURL    string
	botToken   string
	chatID     string
}

func NewTelegramClient(botToken, chatID string) *TelegramClient {
	return &TelegramClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://api.telegram.org",
		botToken:   botToken,
		chatID:     chatID,
	}
}

func (c *TelegramClient) SendStaffMessage(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":                  c.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	// The bot token is part of the URL path — never log this URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.botToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// *url.Error embeds the full URL, i.e. the bot token; don't wrap it.
		return fmt.Errorf("notify: Telegram request failed (network error)")
	}
	defer func() { _ = resp.Body.Close() }()

	var tr struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&tr); err != nil {
		return fmt.Errorf("notify: Telegram response (HTTP %d) not JSON: %w", resp.StatusCode, err)
	}
	if !tr.OK {
		return fmt.Errorf("notify: Telegram sendMessage failed (HTTP %d, code %d): %s", resp.StatusCode, tr.ErrorCode, tr.Description)
	}
	return nil
}
