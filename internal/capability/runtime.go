package capability

import "github.com/verdu/alter/internal/service"

// RegisterShippedCapabilities registers the V1 shipped capabilities into reg,
// backed by the concrete dependencies ALTER already uses (the shared
// TaskService instance). It is the single registration point the runtime calls
// (cmd/alter/main.go): the registry stays the single source of truth for both
// execution (Dispatcher) and the catalog the planner sees (Catalog →
// PlannerContextBuilder), so Pi can only propose capabilities that can actually
// execute. Registration follows Registry.Register semantics (panic on empty
// name, duplicate name or nil handler).
func RegisterShippedCapabilities(reg *Registry, taskSvc *service.TaskService) {
	reg.Register(
		Capability{
			Name:        "list_tasks",
			Description: "List all tasks",
			Parameters:  []byte(`{"type":"object","properties":{}}`),
		},
		NewListTasksHandler(taskSvc),
	)
}
