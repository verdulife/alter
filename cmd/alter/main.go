// Command alter runs the alter runtime: the Telegram inbound adapter
// (commands), the outbound notification channel, the Scheduler and — when Pi is
// enabled — the real Pi Agent via the Orchestrator composite
// (AgentFlow → Channel → Event best-effort). When Pi is not enabled the
// Scheduler action is the Telegram-only NotifyAction; no fake Agent exists in
// production.
//
// Optional semantic search (ALTER_SEARCH_ENABLED) wires the derived vector index
// into that composite: the Orchestrator retrieves related context for the Agent
// and feeds past agent responses back as memory, while SQLite stays the source
// of truth and the index is rebuilt from it at boot. Without an embedding
// provider the capability degrades to unavailable and every consumer behaves
// exactly as V1.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/verdu/alter/internal/adapter/agent"
	"github.com/verdu/alter/internal/adapter/semantic"
	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/application"
	"github.com/verdu/alter/internal/bridge"
	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/scheduler"
	"github.com/verdu/alter/internal/service"
	"github.com/verdu/alter/internal/storage/sqlite"
	"time"
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

	// Semantic search: opt-in via ALTER_SEARCH_ENABLED. It needs an embedding
	// provider, and this phase ships none (NewEmbedder returns nil), so the
	// capability degrades cleanly: when no provider is configured the searcher
	// and indexer stay nil, every consumer behaves exactly as V1, and nothing
	// blocks Tasks, Scheduler, Telegram or the Pi Agent. Only with a real
	// provider is the derived search_docs index wired and rebuilt at boot.
	var searcher domain.SemanticSearcher
	var indexer domain.SemanticIndexer
	if cfg.SearchEnabled {
		embedder, err := semantic.NewEmbedder(semantic.EmbedderConfig{
			Provider: cfg.EmbeddingProvider,
			BaseURL:  cfg.EmbeddingURL,
		})
		if err != nil {
			logger.Printf("runtime: semantic search disabled: %v", err)
		} else if embedder == nil {
			logger.Printf("runtime: semantic search enabled but no embedding provider configured (ALTER_EMBEDDING_PROVIDER); search is not wired and degrades to unavailable")
		} else {
			sem := store.NewSemanticStore(embedder, semantic.WithDefaultLimit(cfg.SearchLimit))
			searcher = sem
			indexer = sem
			logger.Printf("runtime: semantic search enabled (embedder=%s, limit=%d)", embedder.Name(), cfg.SearchLimit)
		}
	}

	client := telegram.NewClient(cfg.TelegramToken)
	channel := telegram.NewChannel(client, cfg.TelegramChatID)

	// Shared TaskService: used by the Telegram commands, the natural language
	// interpretation and the runtime capability registry (list_tasks). It is
	// built before the Scheduler because the runtime action (and with it the
	// capability registry) needs it first; its optional rescan hint is wired
	// below, once the Scheduler exists.
	taskOpts := []service.TaskOption{}
	if indexer != nil {
		taskOpts = append(taskOpts, service.WithTaskIndexer(indexer))
	}
	taskSvc := service.NewTaskService(tasks, triggers, events, taskOpts...)

	// TriggerService and ReminderService are shared like the TaskService: they
	// back every reminder route (natural language one-shot/recurring and
	// /recordar) and, since the reminder refactor, the capability registry seam
	// for create_reminder. They are built here, before the Scheduler
	// and the registry, because the registry needs ReminderService; their
	// optional rescan hints are wired below once the Scheduler exists (the same
	// option-function pattern the TaskService hint uses).
	triggerSvc := service.NewTriggerService(triggers, tasks)
	reminderSvc := service.NewReminderService(taskSvc, triggerSvc)

	// User timezone: parsed once from ALTER_TIMEZONE (falling back to the
	// system timezone) and injected into the capability registry
	// (CreateReminderHandler via WithTimezone), so "today"/"tomorrow"
	// resolution rules use the user's timezone.
	tz := time.Local
	if cfg.Timezone != "" {
		loc, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			logger.Printf("runtime: invalid ALTER_TIMEZONE %q, using system timezone: %v", cfg.Timezone, err)
		} else {
			tz = loc
		}
	}

	// Pi Agent: shared between the Scheduler action (notifications) and the
	// inbound AgentFlow free-text route. Declared here so both can reuse it.
	//
	// The runtime capability registry is built alongside: it is the single
	// source of truth for the shipped capabilities (list_tasks over the shared
	// TaskService), feeding both execution (the Dispatcher below) and the
	// catalog the planner sees (Catalog → PlannerContextBuilder embedded in
	// every Pi request), so Pi only proposes capabilities that can actually
	// execute.
	var registry *capability.Registry
	var piAgent *agent.PiAgent
	if cfg.PiEnabled {
		registry = capability.NewRegistry()
		capability.RegisterShippedCapabilities(registry, taskSvc, reminderSvc, capability.WithTimezone(tz))
		piAgent = agent.NewPiAgent(
			agent.PiConfig{
				Bin:          cfg.PiBin,
				Provider:     cfg.PiProvider,
				Model:        cfg.PiModel,
				Timeout:      cfg.PiTimeout,
				NoTools:      cfg.PiNoTools,
				SystemPrompt: cfg.PiSystemPrompt,
			},
			agent.WithPiLogger(logger),
			agent.WithPlannerContextBuilder(
				capability.NewPlannerContextBuilder(capability.NewCatalog(registry)),
			),
		)
	}

	// Scheduler action: the real Pi Agent composite when explicitly enabled, the
	// Telegram-only NotifyAction otherwise. agentFlow is the shared capability
	// pipeline (Pi Agent + CapabilityFlow) reused by the Scheduler Orchestrator
	// AND the inbound free-text route below, so both entry points see the exact
	// same Agent, catalog and executor.
	var action domain.TriggerAction
	var agentFlow *application.AgentFlow
	if piAgent != nil {
		// Capability runtime pipeline: Registry → Dispatcher → PlanExecutor →
		// CapabilityFlow → AgentFlow. The registry built with the Pi Agent above
		// (carrying the shipped capabilities) is shared unchanged, so the
		// execution side of the Orchestrator and the planner context inside
		// PiAgent see the exact same catalog. registry is non-nil exactly when
		// piAgent is non-nil (they are built together above).
		capabilityFlow := application.NewCapabilityFlow(
			capability.NewPlanExecutor(capability.NewDispatcher(registry)),
		)
		agentFlow = application.NewAgentFlow(piAgent, capabilityFlow)

		opts := []agent.Option{agent.WithLogger(logger)}
		if searcher != nil {
			opts = append(opts, agent.WithSearcher(searcher))
		}
		if indexer != nil {
			opts = append(opts, agent.WithIndexer(indexer))
		}
		action = agent.NewOrchestrator(agentFlow, channel, events, opts...)
		logger.Printf("runtime: scheduler action = Pi Agent (orchestrator via AgentFlow), bin=%q", cfg.PiBin)
	} else {
		action = telegram.NewNotifyAction(channel)
		logger.Printf("runtime: scheduler action = Telegram notifications only (set ALTER_PI_ENABLED=true for the Pi Agent)")
	}

	sched := scheduler.NewScheduler(triggers, tasks, events, action, scheduler.WithLogger(logger))

	// The Scheduler rescan hints (Wake) of the shared services, built above the
	// Scheduler because the runtime capability registry needed them first:
	// WithTaskRescheduler / WithTriggerRescheduler are by design option
	// functions, applied here once the Scheduler exists. The runtime wiring is
	// identical to the previous order — only the construction point of the
	// trigger/reminder services moved above, without their hints, so the
	// registry can receive the shared ReminderService.
	service.WithTaskRescheduler(sched)(taskSvc)
	service.WithTriggerRescheduler(sched)(triggerSvc)

	cmdSvc := commandService{tasks: taskSvc, reminders: reminderSvc}

	// Inbound free text: with the bridge enabled (ALTER_PI_BRIDGE) it is the
	// direct Telegram→Pi path (a persistent pi session, clean loaded, no
	// orchestrator). Otherwise, with the Pi Agent enabled the AgentFlow
	// capability pipeline is the primary route (Pi decides conversational reply
	// vs Plan JSON; a Plan executes registered capabilities: list_tasks,
	// create_task, complete_task, cancel_task, create_reminder). With neither
	// enabled there is no natural handler: the behavior is exactly the pre-slice
	// one (slash commands + help fallback).
	var naturalHandler telegram.NaturalHandler
	var bridgeSession *bridge.Session
	switch {
	case cfg.PiBridgeEnabled:
		bridgeSession = bridge.NewSession(bridge.Config{
			Bin:         cfg.PiBin,
			Provider:    cfg.PiProvider,
			Model:       cfg.PiModel,
			Timeout:     cfg.PiTimeout,
			SessionName: cfg.BridgeSessionName,
		}, bridge.WithLogger(logger))
		// Stream pi's deltas to Telegram as an animated draft; the final text is
		// persisted by the adapter via sendMessage. Each draft update is bounded
		// so a slow Telegram call cannot stall pi's event reader.
		naturalHandler = func(ctx context.Context, text string, stream telegram.Stream) (string, error) {
			return bridgeSession.PromptStream(ctx, text, func(partial string) {
				if stream == nil {
					return
				}
				dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				_ = stream.Update(dctx, partial)
			})
		}
		logger.Printf("runtime: inbound free text = bridge (Telegram→pi directo, sesión persistente, bin=%q)", cfg.PiBin)
	case agentFlow != nil:
		naturalHandler = telegram.NewAgentFlowHandler(agentFlow).Handle
		logger.Printf("runtime: inbound free text = AgentFlow (Pi + capabilities)")
	default:
		logger.Printf("runtime: natural language interpretation disabled (set ALTER_PI_ENABLED=true or ALTER_PI_BRIDGE=true)")
	}

	inbound := telegram.NewAdapter(client, func(ctx context.Context, text string, stream telegram.Stream) (string, error) {
		return telegram.Handle(ctx, cmdSvc, text, stream, naturalHandler)
	}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- inbound.Run(ctx) }()
	go func() { errCh <- sched.Run(ctx) }()

	// Rebuild the derived semantic index in the background: the runtime starts
	// immediately (the index is not a boot dependency) and Reset + reindex from
	// the SQLite source of truth converge any stale/orphaned rows. A failed
	// rebuild leaves a partial index (reads fail open) and the next boot's
	// rebuild converges again.
	if indexer != nil {
		reindexer := service.NewReindexer(tasks, events, indexer, service.WithReindexerLogger(logger))
		go func() {
			if err := reindexer.Rebuild(ctx); err != nil {
				logger.Printf("runtime: semantic index rebuild failed (the next boot rebuilds again): %v", err)
			}
		}()
	}

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
	// Terminate the persistent bridge pi process (if the bridge was wired) so it
	// is not left as an orphan child when the runtime exits.
	if bridgeSession != nil {
		bridgeSession.Close()
	}
	logger.Printf("runtime: shutdown complete")
}
