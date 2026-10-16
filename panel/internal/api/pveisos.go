package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// The ISO library: official installer and tool ISOs (proxmox/isos.yaml),
// each resolved to the vendor's newest file and its checksum. The browser
// then asks Proxmox to download that URL into a storage with the
// existing download endpoint, and Proxmox verifies the hash. URLs only
// ever come from the built-in list, never from the request.

type pveISOEntry struct {
	proxmox.ISO
	*proxmox.ResolvedISO
	Error string `json:"error,omitempty"`
}

var (
	isoMu    sync.Mutex
	isoCache []pveISOEntry
	isoAt    time.Time
)

const isoTTL = time.Hour

func (a *API) handlePVEISOs(w http.ResponseWriter, r *http.Request) {
	isoMu.Lock()
	defer isoMu.Unlock()
	if isoCache != nil && time.Since(isoAt) < isoTTL && r.URL.Query().Get("refresh") == "" {
		writeJSON(w, http.StatusOK, isoCache)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	out := make([]pveISOEntry, len(proxmox.ISOs))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range proxmox.ISOs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			x := proxmox.ISOs[i]
			res, err := x.Resolve(ctx)
			out[i] = pveISOEntry{ISO: x, ResolvedISO: res}
			if err != nil {
				out[i].Error = err.Error()
			}
		}(i)
	}
	wg.Wait()
	isoCache, isoAt = out, time.Now()
	writeJSON(w, http.StatusOK, out)
}
