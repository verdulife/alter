package capability

import "github.com/verdu/alter/internal/service"

// RegisterShippedCapabilities registers the V1 shipped capabilities into reg,
// backed by the concrete dependencies ALTER already uses (the shared TaskService
// and ReminderService instances). It is the single registration point the
// runtime calls (cmd/alter/main.go): the registry stays the single source of
// truth for both execution (Dispatcher) and the catalog the planner sees
// (Catalog → PlannerContextBuilder), so Pi can only propose capabilities that
// can actually execute. Registration follows Registry.Register semantics (panic
// on empty name, duplicate name or nil handler).
//
// reminderSvc is the dependency seam for the create_reminder capability: passing
// the shared ReminderService here means a handler can be registered against it
// without duplicating services.
//
// handlerOpts are forwarded to the create_reminder handler constructor — the
// existing Option pattern (e.g. WithTimezone(tz)) — so the runtime can inject
// the real user timezone from ALTER_TIMEZONE without growing a positional
// parameter per future scheduling case. NewCreateReminderHandler still defaults
// to time.Now/time.Local when no options are passed.
func RegisterShippedCapabilities(reg *Registry, taskSvc *service.TaskService, reminderSvc *service.ReminderService, handlerOpts ...Option) {
	reg.Register(
		Capability{
			Name:        "list_tasks",
			Description: "List all tasks",
			Parameters:  []byte(`{"type":"object","properties":{}}`),
		},
		NewListTasksHandler(taskSvc),
	)

	reg.Register(
		Capability{
			Name:        "create_task",
			Description: "Create a new task",
			Parameters: []byte(`{
				"type": "object",
				"properties": {
					"title":       {"type": "string"},
					"description": {"type": "string"},
					"priority":    {"type": "string"},
					"due_at":      {"type": "string"}
				},
				"required": ["title"]
			}`),
		},
		NewCreateTaskHandler(taskSvc),
	)

	reg.Register(
		Capability{
			Name:        "complete_task",
			Description: "Complete an existing task by textual reference",
			Parameters: []byte(`{
				"type": "object",
				"properties": {
					"task_ref": {"type": "string"}
				},
				"required": ["task_ref"]
			}`),
		},
		NewCompleteTaskHandler(taskSvc),
	)

	reg.Register(
		Capability{
			Name:        "cancel_task",
			Description: "Cancel an existing task by textual reference",
			Parameters: []byte(`{
				"type": "object",
				"properties": {
					"task_ref": {"type": "string"}
				},
				"required": ["task_ref"]
			}`),
		},
		NewCancelTaskHandler(taskSvc),
	)

	reg.Register(
		Capability{
			Name:        "create_reminder",
			Description: "Create a reminder task (one-shot relative, one-shot absolute, or recurring)",
			Parameters: []byte(`{
				"type": "object",
				"properties": {
					"title":         {"type": "string"},
					"relative":      {"type": "string"},
					"absolute_time": {"type": "string"},
					"absolute_date": {"type": "string"},
					"recurrence":    {"type": "object"}
				},
				"required": ["title"]
			}`),
		},
		// All three scheduling cases (relative, absolute, recurring) are
		// implemented; the handler delegates time and calendar derivation to
		// the shared reminder service logic.
		NewCreateReminderHandler(reminderSvc, handlerOpts...),
	)
}
