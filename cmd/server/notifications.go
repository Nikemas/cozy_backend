package main

import (
	"database/sql"
	"log/slog"

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
func buildNotifications(db *sql.DB, cfg *config.Config) *notifications.Dispatcher {
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
		AdminBaseURL: cfg.PublicBaseURL,
	})
}
