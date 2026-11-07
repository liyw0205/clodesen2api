package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

type systemStatus struct {
	Ready         bool                   `json:"ready"`
	Draining      bool                   `json:"draining"`
	Database      bool                   `json:"database"`
	Admission     bool                   `json:"admission"`
	Audit         usage.StorageHealth    `json:"audit"`
	Accounts      upstream.RoutingHealth `json:"accounts"`
	Active        int                    `json:"active"`
	QueuedJobs    int                    `json:"queuedJobs"`
	RunningJobs   int                    `json:"runningJobs"`
	FailedJobs    int                    `json:"failedJobs"`
	JobsAvailable bool                   `json:"jobsAvailable"`
	StartedAt     time.Time              `json:"startedAt"`
	CheckedAt     time.Time              `json:"checkedAt"`
}

func (s *Service) Ready(ctx context.Context) error {
	s.mu.Lock()
	store, capacity, draining := s.control, s.admission, s.draining
	s.mu.Unlock()
	if draining {
		return errors.New("service draining")
	}
	if store != nil {
		if err := store.Ping(ctx); err != nil {
			return errors.New("control database unavailable")
		}
	}
	if err := s.SyncSettings(ctx); err != nil {
		return errors.New("settings unavailable")
	}
	if err := s.SyncAccounts(ctx); err != nil {
		return errors.New("accounts unavailable")
	}
	if capacity != nil {
		if _, err := capacity.Snapshot(ctx); err != nil {
			return errors.New("admission unavailable")
		}
	}
	if s.upstream.RoutingHealth().Available == 0 {
		return errors.New("no available upstream account")
	}
	return nil
}

func (s *Service) SystemStatus(ctx context.Context) systemStatus {
	s.mu.Lock()
	store, capacity, jobStore, draining := s.control, s.admission, s.jobs, s.draining
	s.mu.Unlock()
	view := systemStatus{Draining: draining, Database: store == nil, Audit: s.usage.StorageHealth(), Accounts: s.upstream.RoutingHealth(), StartedAt: s.startedAt, CheckedAt: time.Now()}
	if store != nil {
		view.Database = store.Ping(ctx) == nil
	}
	if capacity != nil {
		snapshot, err := capacity.Snapshot(ctx)
		view.Admission = err == nil
		view.Active = snapshot.Active
	}
	if jobStore != nil {
		jobs, err := jobStore.List(ctx)
		view.JobsAvailable = err == nil
		for _, job := range jobs {
			switch job.State {
			case "queued":
				view.QueuedJobs++
			case "running":
				view.RunningJobs++
			case "failed", "partial", "interrupted":
				view.FailedJobs++
			}
		}
	}
	view.Ready = !draining && view.Database && view.Accounts.Available > 0 && (capacity == nil || view.Admission)
	return view
}

func (s *Service) systemStatusRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.SystemStatus(ctx))
}
