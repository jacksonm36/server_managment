package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Setting a user's password inside a running VM through the QEMU guest
// agent (Proxmox's agent/set-user-password). Nothing is typed into the
// console, so the guest's keyboard layout does not matter: the way back in
// when a console password keeps failing. The password is never logged.

var guestUser = regexp.MustCompile(`^[A-Za-z0-9_.$-][A-Za-z0-9 _.$-]{0,63}$`)

func (a *API) handlePVEGuestPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !guestUser.MatchString(req.Username) {
		writeErr(w, http.StatusBadRequest, "user name: letters, digits, spaces, . _ - $")
		return
	}
	if n := len([]rune(req.Password)); n < 8 || n > 128 || strings.IndexFunc(req.Password, unicode.IsControl) >= 0 {
		writeErr(w, http.StatusBadRequest, "password: 8 to 128 characters, no control characters")
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "containers have no guest agent; use the console (passwd)")
		return
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	form := map[string][]string{"username": {req.Username}, "password": {req.Password}}
	if err := s.Cl.Do(r.Context(), http.MethodPost, proxmox.GuestPath(node, typ, vmid)+"/agent/set-user-password", form, nil); err != nil {
		a.audit(r, "", "proxmox.set_password", target, map[string]string{"username": req.Username, "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, "the guest agent did not set it: "+err.Error())
		return
	}
	a.audit(r, "", "proxmox.set_password", target, map[string]string{"username": req.Username}, true)
	w.WriteHeader(http.StatusNoContent)
}
