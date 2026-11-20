package itop

import (
	"context"
)

const writeComment = "itop-cli"

// WorkOrderInput is the fixed field set of a new WorkOrder. Empty optional
// fields are not sent.
type WorkOrderInput struct {
	Name, DescriptionHTML, TeamID, AgentID, StartDate, EndDate string
}

// privateLogEntry is the only shape a private log write takes.
func privateLogEntry(html string) map[string]any {
	return map[string]any{"add_item": map[string]any{"message": html, "format": "html"}}
}

func refuse(t Target, wanted string) error {
	return &RefusedError{Ref: t.ref, ActualClass: t.class, Wanted: wanted}
}

// resolved reports whether t came from ResolveTarget rather than being a zero
// value.
func (t Target) resolved() bool { return t.id != "" && t.class != "" }

func (c *Client) update(ctx context.Context, t Target, fields map[string]any) error {
	if !t.resolved() {
		return refuse(t, "resolved target")
	}
	_, err := c.post(ctx, "core/update", map[string]any{
		"comment":       writeComment,
		"class":         t.class,
		"key":           t.id,
		"output_fields": "id",
		"fields":        fields,
	})
	return err
}

// SetDescription replaces the whole description of a Change or WorkOrder.
func (c *Client) SetDescription(ctx context.Context, t Target, html string) error {
	if t.kind != TargetChange && t.kind != TargetWorkOrder {
		return refuse(t, "Change or WorkOrder")
	}
	return c.update(ctx, t, map[string]any{"description": html})
}

// AppendPrivateLog appends one entry to the private log of an Incident or
// Change.
func (c *Client) AppendPrivateLog(ctx context.Context, t Target, html string) error {
	if t.kind != TargetIncident && t.kind != TargetChange {
		return refuse(t, "Incident or Change")
	}
	return c.update(ctx, t, map[string]any{"private_log": privateLogEntry(html)})
}

// CreateWorkOrder creates a WorkOrder under a Change and returns its id.
func (c *Client) CreateWorkOrder(ctx context.Context, change Target, wo WorkOrderInput) (string, error) {
	if change.kind != TargetChange {
		return "", refuse(change, "Change")
	}
	if !change.resolved() {
		return "", refuse(change, "resolved target")
	}
	fields := map[string]any{
		"ticket_id":   change.id,
		"name":        wo.Name,
		"description": wo.DescriptionHTML,
	}
	for k, v := range map[string]string{
		"team_id": wo.TeamID, "agent_id": wo.AgentID, "start_date": wo.StartDate, "end_date": wo.EndDate,
	} {
		if v != "" {
			fields[k] = v
		}
	}
	r, err := c.post(ctx, "core/create", map[string]any{
		"comment":       writeComment,
		"class":         "WorkOrder",
		"output_fields": "id",
		"fields":        fields,
	})
	if err != nil {
		return "", err
	}
	if len(r.Objects) == 0 {
		return "", &Error{Kind: ErrAPI, Msg: "iTop created no work order"}
	}
	return string(r.Objects[0].Key), nil
}
