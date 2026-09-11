package application

import (
	"encoding/json"
	"fmt"

	"github.com/verdu/alter/internal/capability"
)

// FlowDeliveryText derives the user-facing delivery text from a FlowResult.
// A conversation carries its text unchanged; a plan carries the executed
// capability results, serialized as JSON (the V1 rendering of a plan outcome).
//
// It is the single presentation seam shared by the Scheduler Orchestrator and
// the inbound free-text AgentFlow handler, so the delivered shape of a plan is
// identical regardless of the entry point.
func FlowDeliveryText(r FlowResult) string {
	if r.Kind == capability.ResponseConversation {
		return r.Response
	}
	results := r.Results
	if results == nil {
		// Defensive: a plan FlowResult always carries a non-nil slice; never
		// serialize "null" into a delivery.
		results = []capability.CapabilityResult{}
	}
	data, err := json.Marshal(results)
	if err != nil {
		// Defensive: capability results are JSON-able by contract; never drop the
		// outcome on an impossible failure.
		return fmt.Sprintf("%v", results)
	}
	return string(data)
}
