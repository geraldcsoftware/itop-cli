package itop

import (
	"context"
	"fmt"
	"strings"
)

// TargetKind is the kind of object a write is allowed to address.
type TargetKind int

const (
	// TargetIncident is an Incident, matched by ref.
	TargetIncident TargetKind = iota
	// TargetChange is a Change or any Change subclass, matched by ref.
	TargetChange
	// TargetWorkOrder is a WorkOrder, matched by numeric id.
	TargetWorkOrder
)

func (k TargetKind) String() string {
	switch k {
	case TargetIncident:
		return "Incident"
	case TargetChange:
		return "Change"
	case TargetWorkOrder:
		return "WorkOrder"
	}
	return "unknown"
}

// Target is an object that ResolveTarget found and classified. It has no
// exported constructor, so a write can only address an object that was looked
// up first.
type Target struct {
	kind   TargetKind
	class  string
	id     string
	ref    string
	status string
}

// Class is the concrete class from the response, for example NormalChange.
func (t Target) Class() string { return t.class }

// ID is the numeric iTop key.
func (t Target) ID() string { return t.id }

// Ref is the ticket ref; for a WorkOrder, which has none, it is the id.
func (t Target) Ref() string { return t.ref }

// Status is the object's current status.
func (t Target) Status() string { return t.status }

// RefusedError reports an object of the wrong class for the requested write.
type RefusedError struct {
	Ref         string
	ActualClass string
	// Wanted names the class or classes the write accepts, without an article.
	Wanted string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s is %s, not %s", e.Ref, withArticle(e.ActualClass), withArticle(e.Wanted))
}

func withArticle(noun string) string {
	if strings.ContainsRune("AEIOU", rune(noun[0])) {
		return "an " + noun
	}
	return "a " + noun
}

// NotFoundError reports that no object matched.
type NotFoundError struct {
	What string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s not found; refs are full, for example I-000123", e.What)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ResolveTarget looks up the object a write will address. When a ref is not
// found as the requested kind but exists as another ticket class, it returns
// *RefusedError; when it does not exist at all, *NotFoundError.
func (c *Client) ResolveTarget(ctx context.Context, kind TargetKind, refOrID string) (Target, error) {
	var oql, fields string
	switch kind {
	case TargetIncident, TargetChange:
		oql = fmt.Sprintf("SELECT %s WHERE ref = %s", kind, Quote(refOrID))
		fields = "ref,status"
	case TargetWorkOrder:
		if !isDigits(refOrID) {
			return Target{}, &NotFoundError{What: fmt.Sprintf("work order %q", refOrID)}
		}
		oql = "SELECT WorkOrder WHERE id = " + refOrID
		fields = "status"
	default:
		return Target{}, &Error{Kind: ErrAPI, Msg: "unknown target kind"}
	}

	objs, err := c.Get(ctx, kind.String(), oql, strings.Split(fields, ","), 1, 0)
	if err != nil {
		return Target{}, err
	}
	if len(objs) > 0 {
		o := objs[0]
		t := Target{kind: kind, class: o.Class, id: o.ID, ref: refOrID}
		if s, ok := o.Fields["status"].(string); ok {
			t.status = s
		}
		return t, nil
	}

	if kind != TargetWorkOrder {
		others, err := c.Get(ctx, "Ticket", "SELECT Ticket WHERE ref = "+Quote(refOrID), []string{"ref"}, 1, 0)
		if err != nil {
			return Target{}, err
		}
		if len(others) > 0 {
			return Target{}, &RefusedError{Ref: refOrID, ActualClass: others[0].Class, Wanted: kind.String()}
		}
	}
	return Target{}, &NotFoundError{What: fmt.Sprintf("%s %q", strings.ToLower(kind.String()), refOrID)}
}
