// Package push delivers mobile push notifications through Firebase Cloud
// Messaging's HTTP v1 API. When FCM isn't configured (no Firebase project
// yet, local dev) NopSender logs instead of sending, so callers never need
// to special-case "push disabled".
package push

import (
	"context"
	"errors"
	"log/slog"
)

// ErrInvalidToken is returned (wrapped) by Sender.Send when FCM reports
// the device token as permanently unusable (app uninstalled, token
// rotated, token from another Firebase project). Callers should delete
// the token so it isn't retried forever.
var ErrInvalidToken = errors.New("push: device token is unregistered or invalid")

// Message is one notification to one device.
type Message struct {
	Title string
	Body  string
	// Data is delivered to the Flutter app alongside the notification
	// (FCM requires string values) — e.g. {"type":"order_status",
	// "order_id":"..."} so a tap can deep-link to the order screen.
	Data map[string]string
	// AndroidChannelID is the Android notification channel the Flutter
	// app created (e.g. "orders"); empty uses FCM's default channel.
	AndroidChannelID string
}

// Sender sends one Message to one FCM registration token.
type Sender interface {
	Send(ctx context.Context, token string, msg Message) error
}

// NopSender is the Sender used when FCM isn't configured: it only logs.
type NopSender struct{}

func (NopSender) Send(_ context.Context, token string, msg Message) error {
	slog.Info("push: FCM not configured, notification not sent",
		"token_suffix", tokenSuffix(token), "title", msg.Title)
	return nil
}

// tokenSuffix keeps FCM tokens (device identifiers) out of logs except for
// a short tail useful for correlating.
func tokenSuffix(token string) string {
	if len(token) <= 6 {
		return token
	}
	return "…" + token[len(token)-6:]
}
