package server

import (
	"context"
	"fmt"
	"time"

	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/rxtech-lab/admin-generator/datasource/gormds"
)

// maintenanceRuleError is a user-facing validation failure. It reports as
// admin.ErrBadInput so the admin HTTP layer returns 400 with the message,
// rather than swallowing it as a generic 500.
type maintenanceRuleError struct{ msg string }

func (e *maintenanceRuleError) Error() string        { return e.msg }
func (e *maintenanceRuleError) Is(target error) bool { return target == admin.ErrBadInput }

var (
	errMaintenanceOngoingExists = &maintenanceRuleError{"another maintenance window is already ongoing; only one ongoing window is allowed"}
	errMaintenanceOverlap       = &maintenanceRuleError{"this maintenance window overlaps an existing one; adjust the start/end times"}
)

// maintenanceDataSource wraps the generic gorm DataSource to enforce the
// maintenance write rules the admin CRUD form cannot express with struct tags:
//
//   - An "ongoing" window's StartAt is forced to the current time (it pauses the
//     app immediately, regardless of the submitted start).
//   - At most one window may be ongoing.
//   - Windows must not overlap in time.
//
// Reads (List/Get/Search) and Delete pass straight through to the embedded
// DataSource; only Create and Update carry validation.
type maintenanceDataSource struct {
	admin.DataSource[Maintenance]
	store *MaintenanceStore
}

func newMaintenanceDataSource(store *MaintenanceStore) *maintenanceDataSource {
	return &maintenanceDataSource{
		DataSource: gormds.New[Maintenance](store.db),
		store:      store,
	}
}

func (d *maintenanceDataSource) Create(ctx context.Context, item *Maintenance) error {
	if err := d.store.prepareForSave(ctx, item, time.Now()); err != nil {
		return err
	}
	return d.DataSource.Create(ctx, item)
}

func (d *maintenanceDataSource) Update(ctx context.Context, id string, patch map[string]any) (Maintenance, error) {
	current, err := d.DataSource.Get(ctx, id)
	if err != nil {
		return Maintenance{}, err
	}
	merged, err := applyMaintenancePatch(current, patch)
	if err != nil {
		return Maintenance{}, err
	}
	if err := d.store.prepareForSave(ctx, &merged, time.Now()); err != nil {
		return Maintenance{}, err
	}
	// prepareForSave may have forced StartAt (ongoing => now); persist that too.
	if merged.Status == MaintenanceStatusOngoing {
		patch["start_at"] = merged.StartAt
	}
	return d.DataSource.Update(ctx, id, patch)
}

// applyMaintenancePatch overlays a JSON patch (as produced by the admin edit
// form: keys are json field names, dates are RFC3339 strings) onto the current
// row, returning the resulting window for validation. Only the fields that
// affect the write rules (status, start_at, end_at) are read.
func applyMaintenancePatch(base Maintenance, patch map[string]any) (Maintenance, error) {
	out := base
	if v, ok := patch["status"]; ok {
		if s, ok := v.(string); ok {
			out.Status = s
		}
	}
	if v, ok := patch["start_at"]; ok {
		t, present, err := toTime(v)
		if err != nil {
			return out, fmt.Errorf("invalid start time: %w", err)
		}
		if present {
			out.StartAt = t
		}
	}
	if v, ok := patch["end_at"]; ok {
		t, present, err := toTime(v)
		if err != nil {
			return out, fmt.Errorf("invalid end time: %w", err)
		}
		if present {
			out.EndAt = &t
		} else {
			out.EndAt = nil
		}
	}
	return out, nil
}

// toTime coerces a JSON-decoded value into a time. It reports present=false for
// nil/empty (a cleared optional field) and errors on an unparseable value.
func toTime(v any) (t time.Time, present bool, err error) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, false, nil
	case time.Time:
		return x, true, nil
	case *time.Time:
		if x == nil {
			return time.Time{}, false, nil
		}
		return *x, true, nil
	case string:
		if x == "" {
			return time.Time{}, false, nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04"} {
			if parsed, perr := time.Parse(layout, x); perr == nil {
				return parsed, true, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("unrecognized time %q", x)
	default:
		return time.Time{}, false, fmt.Errorf("unsupported time value %T", v)
	}
}
