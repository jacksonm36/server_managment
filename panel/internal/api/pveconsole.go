package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest consoles. Opening one is two steps: a POST on the guest (checked
// like every guest route, so tenants only reach their own pool) asks
// Proxmox for a console and returns a one-time session id; the browser
// then opens the WebSocket for that id within consoleTTL, as the same user.
// The panel relays it to Proxmox's vncwebsocket with the tenant's token.
//
// No console credential reaches the browser: the panel answers the
// display's VNC authentication itself (rfbproxy.go) and logs in to
// termproxy for text consoles (containers, serial ports, node shells),
// which speak the terminal page's JSON protocol.
//
// A session id works once, within consoleTTL, from the same login session
// (not just the same user) and the same address. An open console is
// re-checked every consoleRecheck: signing out, a password change, losing
// the permission or a disabled account closes it, and none lasts longer
// than consoleMaxLife.

const (
	consoleTTL      = 30 * time.Second
	consoleMaxLife  = 12 * time.Hour
	consolePerUser  = 8   // open plus pending, per user
	consolesPending = 256 // across the panel
)

var consoleRecheck = 30 * time.Second

type pveConsole struct {
	admin   int64
	session []byte // hash of the login session's cookie
	ip      string
	perm    string // what the user must still hold while it is open
	who     Who
	cl      *proxmox.Client
	node    string
	typ     string
	vmid    int
	target  string
	proxy   *proxmox.ConsoleProxy
	term    bool
	expires time.Time
}

// addConsole files a console session for the WebSocket to pick up and
// returns its id, or a reason it can't.
func (a *API) addConsole(r *http.Request, c *pveConsole) (string, string) {
	ck, err := r.Cookie(sessionCookie)
	ad := adminFrom(r.Context())
	if err != nil || ad == nil {
		return "", "not logged in"
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "cannot start the console"
	}
	sid := hex.EncodeToString(b)
	c.admin, c.session, c.ip, c.who = ad.ID, hashToken(ck.Value), clientIP(r), whoFrom(r)
	if c.perm == "" {
		c.perm = "proxmox.console"
	}
	c.expires = time.Now().Add(consoleTTL)
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	if a.pveConsoles == nil {
		a.pveConsoles = map[string]*pveConsole{}
	}
	mine := a.pveOpen[ad.ID]
	for k, v := range a.pveConsoles {
		if time.Now().After(v.expires) {
			delete(a.pveConsoles, k)
		} else if v.admin == ad.ID {
			mine++
		}
	}
	if mine >= consolePerUser {
		return "", "too many consoles open; close one first"
	}
	if len(a.pveConsoles) >= consolesPending {
		return "", "the panel is busy opening consoles; try again"
	}
	a.pveConsoles[sid] = c
	return sid, ""
}

// consoleStillAllowed is the periodic check on an open console.
func (a *API) consoleStillAllowed(ctx context.Context, c *pveConsole) bool {
	ad, err := a.Store.SessionAdmin(ctx, c.session)
	if err != nil || ad.ID != c.admin || ad.MustChangePassword {
		return false
	}
	ac, err := a.loadAccess(ctx, ad)
	return err == nil && ac.Can(c.perm)
}

var pveSerialKey = regexp.MustCompile(`^serial[0-3]$`)

func (a *API) handlePVEConsole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"` // auto | vga | serial
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var cfg map[string]any
	if err := s.Cl.Do(ctx, http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/config", nil, &cfg); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	mode, serial := "term", ""
	if typ == "qemu" {
		var serials []string
		for k := range cfg {
			if pveSerialKey.MatchString(k) {
				serials = append(serials, k)
			}
		}
		slices.Sort(serials)
		// A VM whose display is a serial port ("vga: serial0") shows
		// nothing over VNC.
		vga, _ := cfg["vga"].(string)
		if v := strings.SplitN(vga, ",", 2)[0]; slices.Contains(serials, v) {
			serial = v
		} else if len(serials) > 0 {
			serial = serials[0]
		}
		switch req.Mode {
		case "", "auto":
			mode = "vga"
			if strings.HasPrefix(vga, "serial") && serial != "" {
				mode = "term"
			}
		case "vga":
			mode = "vga"
		case "serial":
			if serial == "" {
				writeErr(w, http.StatusBadRequest, "this VM has no serial port")
				return
			}
		default:
			writeErr(w, http.StatusBadRequest, "mode: auto, vga or serial")
			return
		}
		if mode == "vga" {
			serial = ""
		}
	}
	p, err := s.Cl.OpenConsole(ctx, node, typ, vmid, mode == "term", serial)
	if err != nil {
		a.audit(r, "", "proxmox.console", fmt.Sprintf("%s/%d", s.C.Name, vmid), map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sid, msg := a.addConsole(r, &pveConsole{
		cl: s.Cl, node: node, typ: typ, vmid: vmid, target: fmt.Sprintf("%s/%d", s.C.Name, vmid),
		proxy: p, term: mode == "term",
	})
	if msg != "" {
		writeErr(w, http.StatusTooManyRequests, msg)
		return
	}
	out := map[string]any{"sid": sid, "mode": mode}
	if serial != "" {
		out["serial"] = serial
	}
	writeJSON(w, http.StatusOK, out)
}

// takeConsole returns a console session once, for the login session and
// address that opened it, and counts it as open.
func (a *API) takeConsole(r *http.Request) *pveConsole {
	sid := r.PathValue("sid")
	ad := adminFrom(r.Context())
	ck, err := r.Cookie(sessionCookie)
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	c := a.pveConsoles[sid]
	if c == nil || ad == nil || err != nil || c.admin != ad.ID ||
		subtle.ConstantTimeCompare(c.session, hashToken(ck.Value)) != 1 || c.ip != clientIP(r) {
		return nil
	}
	delete(a.pveConsoles, sid)
	if time.Now().After(c.expires) {
		return nil
	}
	if a.pveOpen == nil {
		a.pveOpen = map[int64]int{}
	}
	a.pveOpen[c.admin]++
	return c
}

func (a *API) consoleClosed(c *pveConsole) {
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	if a.pveOpen[c.admin]--; a.pveOpen[c.admin] <= 0 {
		delete(a.pveOpen, c.admin)
	}
}

func (a *API) handlePVEConsoleWS(w http.ResponseWriter, r *http.Request) {
	c := a.takeConsole(r)
	if c == nil {
		writeErr(w, http.StatusNotFound, "console session missing or expired")
		return
	}
	defer a.consoleClosed(c)
	dctx, dcancel := context.WithTimeout(r.Context(), 20*time.Second)
	upstream, err := c.cl.DialConsole(dctx, c.node, c.typ, c.vmid, c.proxy)
	dcancel()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer upstream.CloseNow()
	up := &wsStream{c: upstream}
	if c.term {
		err = termLogin(r.Context(), upstream, c.proxy)
	} else {
		pw := c.proxy.Password
		if pw == "" {
			pw = c.proxy.Ticket
		}
		err = rfbLoginUpstream(r.Context(), up, pw)
	}
	if err != nil {
		a.auditAs(r.Context(), c.who, "proxmox.console", c.target, map[string]any{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Same-origin only (the library checks Origin against Host), so another
	// site can't open a console with the user's cookie.
	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(1 << 20)
	br := &wsStream{c: browser}
	if !c.term {
		if err := rfbGreetBrowser(r.Context(), br); err != nil {
			_ = browser.Close(websocket.StatusProtocolError, "console handshake failed")
			return
		}
		// Anything either side sent past the handshake goes on as is.
		if b := up.rest(); len(b) > 0 && browser.Write(r.Context(), websocket.MessageBinary, b) != nil {
			return
		}
		if b := br.rest(); len(b) > 0 && upstream.Write(r.Context(), websocket.MessageBinary, b) != nil {
			return
		}
	}

	started := time.Now()
	a.auditAs(r.Context(), c.who, "proxmox.console", c.target, map[string]any{"text": c.term}, true)
	defer func() {
		a.auditAs(context.Background(), c.who, "proxmox.console_close", c.target, map[string]any{"seconds": int(time.Since(started).Seconds())}, true)
	}()

	ctx, cancel := context.WithTimeout(r.Context(), consoleMaxLife)
	defer cancel()
	// Close the console when the user's login or permission goes away.
	go func() {
		t := time.NewTicker(consoleRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !a.consoleStillAllowed(ctx, c) {
					_ = browser.Close(websocket.StatusPolicyViolation, "signed out or access removed")
					cancel()
					return
				}
			}
		}
	}()
	// Proxmox -> browser: console output or RFB, as binary frames.
	go func() {
		defer cancel()
		for {
			_, data, err := upstream.Read(ctx)
			if err != nil {
				return
			}
			if browser.Write(ctx, websocket.MessageBinary, data) != nil {
				return
			}
		}
	}()
	if c.term {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					_ = upstream.Write(ctx, websocket.MessageBinary, []byte("2"))
				}
			}
		}()
	}
	// Browser -> Proxmox. Proxmox only accepts binary frames.
	for {
		_, data, err := browser.Read(ctx)
		if err != nil {
			break
		}
		if c.term {
			if data = termFrame(data); data == nil {
				continue
			}
		}
		if upstream.Write(ctx, websocket.MessageBinary, data) != nil {
			break
		}
	}
	_ = browser.Close(websocket.StatusNormalClosure, "")
	_ = upstream.Close(websocket.StatusNormalClosure, "")
}

// termLogin authenticates to termproxy with the ticket, which answers "OK".
func termLogin(ctx context.Context, c *websocket.Conn, p *proxmox.ConsoleProxy) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, []byte(p.User+":"+p.Ticket+"\n")); err != nil {
		return fmt.Errorf("proxmox console: %w", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil || !strings.HasPrefix(string(data), "OK") {
		return fmt.Errorf("proxmox console: login refused")
	}
	return nil
}

// termFrame turns the terminal page's {"type":"input"|"resize"} messages
// into termproxy frames: "0:<bytes>:<data>" and "1:<cols>:<rows>:".
func termFrame(raw []byte) []byte {
	var m struct {
		Type string `json:"type"`
		Data string `json:"data"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	switch m.Type {
	case "input":
		return []byte(fmt.Sprintf("0:%d:%s", len(m.Data), m.Data))
	case "resize":
		if m.Cols < 1 || m.Rows < 1 || m.Cols > 1000 || m.Rows > 1000 {
			return nil
		}
		return []byte(fmt.Sprintf("1:%d:%d:", m.Cols, m.Rows))
	}
	return nil
}
