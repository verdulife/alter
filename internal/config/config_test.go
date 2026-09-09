package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ALTER_DB_PATH", "")
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")

	cfg := Load()
	if cfg.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", cfg.Version)
	}
	if cfg.DBPath != "alter.db" {
		t.Errorf("DBPath = %q, want alter.db", cfg.DBPath)
	}
	if cfg.TelegramToken != "" {
		t.Errorf("TelegramToken = %q, want empty", cfg.TelegramToken)
	}
	if cfg.TelegramChatID != 0 {
		t.Errorf("TelegramChatID = %d, want 0", cfg.TelegramChatID)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("ALTER_DB_PATH", "/tmp/alter.db")
	t.Setenv("TELEGRAM_TOKEN", "secret")
	t.Setenv("TELEGRAM_CHAT_ID", "123456789")

	cfg := Load()
	if cfg.DBPath != "/tmp/alter.db" {
		t.Errorf("DBPath = %q, want /tmp/alter.db", cfg.DBPath)
	}
	if cfg.TelegramToken != "secret" {
		t.Errorf("TelegramToken = %q, want secret", cfg.TelegramToken)
	}
	if cfg.TelegramChatID != 123456789 {
		t.Errorf("TelegramChatID = %d, want 123456789", cfg.TelegramChatID)
	}
}

func TestLoadMalformedChatIDFallsBack(t *testing.T) {
	t.Setenv("TELEGRAM_CHAT_ID", "not-a-number")
	cfg := Load()
	if cfg.TelegramChatID != 0 {
		t.Errorf("TelegramChatID = %d, want 0 on malformed env", cfg.TelegramChatID)
	}
}
