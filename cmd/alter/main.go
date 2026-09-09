// Command alter runs the alter runtime: the Telegram inbound adapter
// (commands), the outbound notification channel, the Scheduler and — when Pi is
// enabled — the real Pi Agent via the Orchestrator composite
// (Agent → Channel → Event best-effort). When Pi is not enabled the Scheduler
// action is the Telegram-only NotifyAction; no fake Agent exists in production.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/verdu/alter/internal/adapter/agent"
	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/scheduler"
	"github.com/verdu/alter/internal/service"
	"github.com/verdu/alter/internal/storage/sqlite"
)

func main() {
	logger := log.New(os.Stderr, "alter: ", log.LstdFlags)

	cfg := config.Load()
	if cfg.TelegramToken == "" {
		logger.Fatal("TELEGRAM_TOKEN is required")
	}
	if cfg.TelegramChatID == 0 {
		logger.Fatal("TELEGRAM_CHAT_ID is required")
	}

	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		logger.Fatalf("open storage: %v", err)
	}
	defer store.Close()

	tasks := store.NewTaskRepository()
	triggers := store.NewTriggerRepository()
	events := store.NewEventStore()

	client := telegram.NewClient(cfg.TelegramToken)
	channel := telegram.NewChannel(client, cfg.TelegramChatID)

	// Scheduler action: the real Pi Agent composite when explicitly enabled, the
	// Telegram-only NotifyAction otherwise.
	var action domain.TriggerAction
	if cfg.PiEnabled {
		piAgent := agent.NewPiAgent(agent.PiConfig{
			Bin:          cfg.PiBin,
			Provider:     cfg.PiProvider,
			Model:        cfg.PiModel,
			Timeout:      cfg.PiTimeout,
			NoTools:      cfg.PiNoTools,
			SystemPrompt: cfg.PiSystemPrompt,
		}, agent.WithPiLogger(logger))
		action = agent.NewOrchestrator(piAgent, channel, events, agent.WithLogger(logger))
		logger.Printf("runtime: scheduler action = Pi Agent (orchestrator), bin=%q", cfg.PiBin)
	} else {
		action = telegram.NewNotifyAction(channel)
		logger.Printf("runtime: scheduler action = Telegram notifications only (set ALTER_PI_ENABLED=true for the Pi Agent)")
	}

	sched := scheduler.NewScheduler(triggers, tasks, events, action, scheduler.WithLogger(logger))

	taskSvc := service.NewTaskService(tasks, triggers, events, service.WithTaskRescheduler(sched))
	triggerSvc := service.NewTriggerService(triggers, tasks, service.WithTriggerRescheduler(sched))

	cmdSvc := commandService{tasks: taskSvc, triggers: triggerSvc}
	inbound := telegram.NewAdapter(client, func(ctx context.Context, text string) (string, error) {
		return telegram.Handle(ctx, cmdSvc, text)
	}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- inbound.Run(ctx) }()
	go func() { errCh <- sched.Run(ctx) }()

	logger.Printf("runtime: alter %s started (db=%s)", cfg.Version, cfg.DBPath)

	// Wait for the first loop to exit, then ask the other one to stop and wait
	// for it too, so shutdown is deterministic. context.Canceled is the normal
	// shutdown signal, not a failure.
	first := <-errCh
	stop()
	second := <-errCh
	if first != nil && !errors.Is(first, context.Canceled) {
		logger.Printf("runtime: %v", first)
	}
	if second != nil && !errors.Is(second, context.Canceled) {
		logger.Printf("runtime: %v", second)
	}
	logger.Printf("runtime: shutdown complete")
}
