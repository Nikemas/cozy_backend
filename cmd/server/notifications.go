package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/notifications"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/push"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// buildNotifications wires the orders.Notifier implementation. Unset or
// broken FCM/Telegram settings degrade to logging no-ops instead of
// stopping the server: notifications are a side channel, and the shop
// must keep taking orders without them (e.g. before the Firebase project
// exists).
func buildNotifications(db *sql.DB, cfg *config.Config, pushSender push.Sender) (*notifications.Dispatcher, error) {
	smsFallback, err := smsStatusFallbackFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	// No plain-SMS provider exists yet (see notify.SMSSender) — with the
	// flag on, fallback SMS are only logged.
	var smsSender notify.SMSSender = notify.NopSMSSender{}
	if smsFallback {
		slog.Warn("notify: SMS_STATUS_FALLBACK is on, but no plain-SMS provider is configured — fallback SMS are only logged")
	}

	var staffMessenger notify.StaffMessenger = notify.NopStaffMessenger{}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		staffMessenger = notify.NewTelegramClient(cfg.TelegramBotToken, cfg.TelegramChatID)
		slog.Info("notify: Telegram staff notifications enabled")
	} else {
		slog.Warn("notify: TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set, new-order alerts are only logged")
	}

	return notifications.NewDispatcher(notifications.Config{
		Push:         pushSender,
		Tokens:       storefront.NewDeviceTokenRepo(db),
		Staff:        staffMessenger,
		Lookup:       notifications.NewSQLOrderInfoLookup(db),
		Contacts:     notifications.NewSQLContactLookup(db),
		SMS:          smsSender,
		SMSFallback:  smsFallback,
		AdminBaseURL: cfg.PublicBaseURL,
	}), nil
}

// smsStatusFallbackFromEnv parses SMS_STATUS_FALLBACK (default false).
func smsStatusFallbackFromEnv(lookup func(string) (string, bool)) (bool, error) {
	v, ok := lookup("SMS_STATUS_FALLBACK")
	if !ok || strings.TrimSpace(v) == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("SMS_STATUS_FALLBACK must be true or false, got %q", v)
	}
	return b, nil
}

// buildPushSender returns the FCM sender, or a logging no-op when FCM
// isn't configured. Shared by order-status notifications and promo
// broadcasts.
func buildPushSender(cfg *config.Config) push.Sender {
	var pushSender push.Sender = push.NopSender{}
	if cfg.FCMCredentialsFile != "" {
		fcm, err := push.NewFCMSenderFromFile(cfg.FCMCredentialsFile, cfg.FCMProjectID)
		if err != nil {
			slog.Error("push: FCM misconfigured, push notifications disabled", "err", err)
		} else {
			slog.Info("push: FCM enabled", "project_id", fcm.ProjectID())
			pushSender = fcm
		}
	} else {
		slog.Warn("push: FCM_CREDENTIALS_FILE not set, push notifications are only logged")
	}

	return pushSender
}
