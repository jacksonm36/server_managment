package api

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/catalog"
)

const (
	catalogSetting      = "catalog_pinned"
	catalogRefreshEvery = 7 * 24 * time.Hour
)

type catalogStored struct {
	LastRun time.Time                  `json:"last_run"`
	Pinned  map[string]*catalog.Pinned `json:"pinned"`
}

// catalogRefresh tracks a running "Refresh catalog".
type catalogRefresh struct {
	mu      sync.Mutex
	running bool
	done    int
	total   int
	lastRun time.Time
}

var catRefresh catalogRefresh

// LoadCatalog installs the last refresh results from the database.
func (a *API) LoadCatalog(ctx context.Context) {
	var st catalogStored
	if err := a.Store.GetSetting(ctx, catalogSetting, &st); err != nil {
		a.Log.Warn("catalog: loading refresh results", "err", err)
		return
	}
	if st.Pinned == nil {
		st.Pinned = map[string]*catalog.Pinned{}
	}
	catalog.SetPinned(st.Pinned)
	catRefresh.mu.Lock()
	catRefresh.lastRun = st.LastRun
	catRefresh.mu.Unlock()
}

// RunCatalogRefresh refreshes the app catalog shortly after start-up when it
// was never refreshed, and then weekly.
func (a *API) RunCatalogRefresh(ctx context.Context) {
	a.LoadCatalog(ctx)
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-tick.C:
		}
		catRefresh.mu.Lock()
		due := time.Since(catRefresh.lastRun) > catalogRefreshEvery
		catRefresh.mu.Unlock()
		if due {
			a.refreshCatalog(ctx)
		}
	}
}

// refreshCatalog pins every source; it returns false if one is already running.
func (a *API) refreshCatalog(ctx context.Context) bool {
	catRefresh.mu.Lock()
	if catRefresh.running {
		catRefresh.mu.Unlock()
		return false
	}
	catRefresh.running, catRefresh.done, catRefresh.total = true, 0, len(catalog.Sources())
	catRefresh.mu.Unlock()
	defer func() {
		catRefresh.mu.Lock()
		catRefresh.running = false
		catRefresh.mu.Unlock()
	}()

	start := time.Now()
	r := &catalog.Refresher{Token: os.Getenv("SM_GITHUB_TOKEN")}
	pinned := r.Refresh(ctx, catalog.PinnedState(), func(done, total int) {
		catRefresh.mu.Lock()
		catRefresh.done = done
		catRefresh.mu.Unlock()
	})
	ok := 0
	for _, p := range pinned {
		if p.SHA256 != "" {
			ok++
		}
	}
	now := time.Now().UTC()
	if err := a.Store.SetSetting(ctx, catalogSetting, catalogStored{LastRun: now, Pinned: pinned}); err != nil {
		a.Log.Error("catalog: saving refresh results", "err", err)
	}
	catalog.SetPinned(pinned)
	catRefresh.mu.Lock()
	catRefresh.lastRun = now
	catRefresh.mu.Unlock()
	a.Log.Info("catalog refreshed", "apps", ok, "sources", len(pinned), "took", time.Since(start).Round(time.Second))
	return true
}

func (a *API) handleCatalogStatus(w http.ResponseWriter, r *http.Request) {
	pinned := catalog.PinnedState()
	type row struct {
		*catalog.Source
		Pinned *catalog.Pinned `json:"pinned,omitempty"`
	}
	rows := make([]row, 0, len(catalog.Sources()))
	for _, s := range catalog.Sources() {
		rows = append(rows, row{s, pinned[s.ID]})
	}
	catRefresh.mu.Lock()
	defer catRefresh.mu.Unlock()
	var last *time.Time
	if !catRefresh.lastRun.IsZero() {
		t := catRefresh.lastRun
		last = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": catRefresh.running, "done": catRefresh.done, "total": catRefresh.total,
		"last_run": last, "sources": rows,
	})
}

func (a *API) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	catRefresh.mu.Lock()
	running := catRefresh.running
	catRefresh.mu.Unlock()
	if running {
		writeErr(w, http.StatusConflict, "a catalog refresh is already running")
		return
	}
	go a.refreshCatalog(a.Ctx)
	a.audit(r, "", "catalog.refresh", "", nil, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
