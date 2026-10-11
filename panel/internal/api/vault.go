package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
	"github.com/jacksonm36/servermgr/panel/internal/vault"
)

// sealSecret seals a stored secret (API tokens, notification settings,
// OpenTofu variables and state, SSH keys) with the vault when it is set up,
// otherwise with the panel key. TOTP secrets always use the panel key, so
// admins can still sign in to unlock the vault after a restart.
func (a *API) sealSecret(plain []byte) (string, error) {
	if a.Vault != nil && a.Vault.Configured() {
		return a.Vault.Seal(plain)
	}
	return a.seal(plain)
}

func (a *API) openSecret(sealed string) ([]byte, error) {
	if vault.IsSealed(sealed) {
		if a.Vault == nil {
			return nil, vault.ErrNotConfigured
		}
		return a.Vault.Open(sealed)
	}
	return a.open(sealed)
}

// secretErr reports a seal/open failure; a locked vault is the caller's to fix.
func (a *API) secretErr(w http.ResponseWriter, err error) {
	if errors.Is(err, vault.ErrLocked) || errors.Is(err, vault.ErrNotConfigured) {
		writeErr(w, http.StatusLocked, err.Error())
		return
	}
	a.internal(w, err)
}

type vaultStatus struct {
	Configured bool `json:"configured"`
	Unlocked   bool `json:"unlocked"`
}

func (a *API) vaultStatus() vaultStatus {
	if a.Vault == nil {
		return vaultStatus{}
	}
	return vaultStatus{Configured: a.Vault.Configured(), Unlocked: a.Vault.Unlocked()}
}

func (a *API) handleVaultStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.vaultStatus())
}

// handleVaultSetup creates the vault and moves every existing secret into it.
func (a *API) handleVaultSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Vault.Configured() {
		writeErr(w, http.StatusConflict, "the vault is already set up")
		return
	}
	v, hdr, recovery, err := vault.Create(req.Passphrase)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := a.Store.SetupVault(r.Context(), hdr, func(old string) (string, error) {
		if vault.IsSealed(old) {
			return old, nil
		}
		plain, err := a.open(old)
		if err != nil {
			return "", err
		}
		return v.Seal(plain)
	})
	if errors.Is(err, store.ErrExists) {
		writeErr(w, http.StatusConflict, "the vault is already set up")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.Vault.Adopt(v)
	a.audit(r, "", "vault.setup", "", map[string]any{"secrets_moved": n}, true)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_key": recovery, "secrets_moved": n})
}

func (a *API) handleVaultUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := a.Vault.Unlock(req.Passphrase); err != nil {
		a.audit(r, "", "vault.unlock", "", nil, false)
		if errors.Is(err, vault.ErrBadPassphrase) {
			time.Sleep(time.Second) // slow down guessing
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	a.audit(r, "", "vault.unlock", "", nil, true)
	writeJSON(w, http.StatusOK, a.vaultStatus())
}

func (a *API) handleVaultLock(w http.ResponseWriter, r *http.Request) {
	a.Vault.Lock()
	a.audit(r, "", "vault.lock", "", nil, true)
	writeJSON(w, http.StatusOK, a.vaultStatus())
}

func (a *API) handleVaultPassphrase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	hdr, err := a.Vault.ChangePassphrase(req.Current, req.New)
	switch {
	case errors.Is(err, vault.ErrBadPassphrase):
		time.Sleep(time.Second)
		writeErr(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, vault.ErrLocked), errors.Is(err, vault.ErrNotConfigured):
		writeErr(w, http.StatusLocked, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Store.SaveVaultHeader(r.Context(), hdr); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "vault.passphrase", "", nil, true)
	w.WriteHeader(http.StatusNoContent)
}
