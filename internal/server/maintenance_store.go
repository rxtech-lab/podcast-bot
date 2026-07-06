package server

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Maintenance status values.
const (
	MaintenanceStatusScheduled = "scheduled"
	MaintenanceStatusOngoing   = "ongoing"
	MaintenanceStatusFinished  = "finished"
)

// Maintenance is one scheduled maintenance window. It drives both the admin
// CRUD resource (via struct tags) and the precheck/config gating: the app is
// paused while a window is active (status != finished and the current time is
// within [StartAt, EndAt]).
type Maintenance struct {
	ID uint `json:"id" jsonschema:"title=ID" table:"order=0;pinned=true"`
	// Title is a short admin-facing label.
	Title string `json:"title" jsonschema:"title=Title" table:"order=1"`
	// Message is shown to users while the window is active.
	Message string `json:"message" jsonschema:"title=Message" table:"order=2"`
	// Status is one of scheduled|ongoing|finished. Setting it to finished
	// resumes the app even if the window's time range has not elapsed.
	Status string `json:"status" jsonschema:"title=Status" table:"order=3;format=chip"`
	// StartAt is when the window begins; the app pauses at/after this time.
	StartAt time.Time `json:"start_at" jsonschema:"title=Start time,format=date-time" table:"order=4;format=date-time"`
	// EndAt optionally bounds the window; nil means "until finished".
	EndAt     *time.Time `json:"end_at" jsonschema:"title=End time,format=date-time" table:"order=5;format=date-time"`
	CreatedAt time.Time  `json:"created_at" jsonschema:"title=Created" table:"order=6;format=date-time"`
	UpdatedAt time.Time  `json:"updated_at" table:"omit=true"`
}

func (Maintenance) TableName() string { return "maintenance_windows" }

// maintenanceForm is the DTO reflected into the admin create/edit form. It omits
// the server-managed id/timestamps so the form only exposes editable fields.
type maintenanceForm struct {
	Title   string     `json:"title" jsonschema:"title=Title" validate:"required"`
	Message string     `json:"message" jsonschema:"title=Message shown to users" uischema:"widget=textarea"`
	Status  string     `json:"status" jsonschema:"title=Status,enum=scheduled,enum=ongoing,enum=finished,default=scheduled" validate:"required,oneof=scheduled ongoing finished"`
	StartAt time.Time  `json:"start_at" jsonschema:"title=Start time,format=date-time" validate:"required"`
	EndAt   *time.Time `json:"end_at" jsonschema:"title=End time (optional)" uischema:"help=Leave empty to keep the app paused until you mark this finished."`
}

// MaintenanceStore persists maintenance windows. It reuses the JobRegistry's
// GORM handle so it lives in the same database, and AutoMigrates its table.
type MaintenanceStore struct {
	db *gorm.DB
}

// NewMaintenanceStore builds the store on the given GORM handle.
func NewMaintenanceStore(db *gorm.DB) (*MaintenanceStore, error) {
	if db == nil {
		return nil, errors.New("maintenance store: nil db")
	}
	if err := db.AutoMigrate(&Maintenance{}); err != nil {
		return nil, err
	}
	return &MaintenanceStore{db: db}, nil
}

// Active returns the maintenance window currently pausing the app, if any. The
// pause is driven purely by status: a window with status "ongoing" pauses the
// app regardless of its start/end times (an operator flips it on immediately,
// and it stays paused until marked "finished"). At most one ongoing window
// exists (enforced on write), so the earliest is returned deterministically.
// The now parameter is unused but kept for call-site symmetry with Upcoming.
func (s *MaintenanceStore) Active(ctx context.Context, _ time.Time) (*Maintenance, bool) {
	if s == nil || s.db == nil {
		return nil, false
	}
	var m Maintenance
	err := s.db.WithContext(ctx).
		Where("status = ?", MaintenanceStatusOngoing).
		Order("start_at asc").
		First(&m).Error
	if err != nil {
		return nil, false
	}
	return &m, true
}

// Upcoming returns a scheduled window to advertise as a heads-up, if any. A
// "scheduled" window never pauses the app (even past its start time) — it only
// informs users a pause is planned — so it is surfaced via Upcoming, not Active.
// The soonest-starting scheduled window wins.
func (s *MaintenanceStore) Upcoming(ctx context.Context, _ time.Time) (*Maintenance, bool) {
	if s == nil || s.db == nil {
		return nil, false
	}
	var m Maintenance
	err := s.db.WithContext(ctx).
		Where("status = ?", MaintenanceStatusScheduled).
		Order("start_at asc").
		First(&m).Error
	if err != nil {
		return nil, false
	}
	return &m, true
}

// prepareForSave normalizes and validates a window about to be persisted. It
// mutates m (an "ongoing" window's StartAt is forced to now) and enforces the
// invariants: at most one ongoing window, and no time-range overlap with any
// other non-finished window. excludeID is the row's own id on update (0 on
// create) so it is not compared against itself.
func (s *MaintenanceStore) prepareForSave(ctx context.Context, m *Maintenance, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("maintenance store: nil db")
	}
	if m.Status == MaintenanceStatusOngoing {
		// An ongoing window pauses the app immediately, so its start is "now".
		m.StartAt = now
		other, err := s.otherOngoingExists(ctx, m.ID)
		if err != nil {
			return err
		}
		if other {
			return errMaintenanceOngoingExists
		}
	}
	// Overlap only matters for windows that are still live (scheduled/ongoing);
	// a finished window is inert and can share any time range.
	if m.Status != MaintenanceStatusFinished {
		overlaps, err := s.overlaps(ctx, m.ID, m.StartAt, m.EndAt)
		if err != nil {
			return err
		}
		if overlaps {
			return errMaintenanceOverlap
		}
	}
	return nil
}

// otherOngoingExists reports whether a different row already has status ongoing.
func (s *MaintenanceStore) otherOngoingExists(ctx context.Context, excludeID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&Maintenance{}).
		Where("status = ?", MaintenanceStatusOngoing).
		Where("id <> ?", excludeID).
		Count(&count).Error
	return count > 0, err
}

// overlaps reports whether the candidate range [start, end] (end nil = open-
// ended) intersects any other non-finished window. Two ranges overlap iff each
// starts at/before the other ends; a nil end is treated as +infinity.
func (s *MaintenanceStore) overlaps(ctx context.Context, excludeID uint, start time.Time, end *time.Time) (bool, error) {
	q := s.db.WithContext(ctx).Model(&Maintenance{}).
		Where("status <> ?", MaintenanceStatusFinished).
		Where("id <> ?", excludeID).
		// candidate.start <= existing.end (existing open-ended → always true)
		Where("end_at IS NULL OR end_at >= ?", start)
	if end != nil {
		// existing.start <= candidate.end (candidate open-ended → skip: always true)
		q = q.Where("start_at <= ?", *end)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
