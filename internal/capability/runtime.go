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
// without duplicating services or reaching into naturalintent. The handler owns
// its own clock/timezone injection (NewCreateReminderHandler defaults to
// time.Now/time.Local) so the registration point stays dependency-free.
func RegisterShippedCapabilities(reg *Registry, taskSvc *service.TaskService, reminderSvc *service.ReminderService) {
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
		// Only the one-shot relative case is implemented in this step: the handler
		// rejects absolute_time/absolute_date/recurrence with ErrInvalidArgs until
		// those cases are implemented (the schema keeps the full contract).
		NewCreateReminderHandler(reminderSvc),
	)
}
