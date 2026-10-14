package api

import (
	"context"
	"crypto/rand"
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
// Graphical consoles relay RFB untouched for noVNC, which needs the VNC
// ticket as its password (it only opens that one console port, and only
// briefly). Text consoles (containers, serial ports) speak the terminal
// page's JSON protocol; the panel logs in to termproxy itself, so their
// ticket never reaches the browser.

const consoleTTL = 30 * time.Second

type pveConsole struct {
	admin   int64
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
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot start the console")
		return
	}
	sid := hex.EncodeToString(b)
	c := &pveConsole{
		admin: adminFrom(r.Context()).ID, who: whoFrom(r), cl: s.Cl,
		node: node, typ: typ, vmid: vmid, target: fmt.Sprintf("%s/%d", s.C.Name, vmid),
		proxy: p, term: mode == "term", expires: time.Now().Add(consoleTTL),
	}
	a.pveMu.Lock()
	if a.pveConsoles == nil {
		a.pveConsoles = map[string]*pveConsole{}
	}
	for k, v := range a.pveConsoles {
		if time.Now().After(v.expires) {
			delete(a.pveConsoles, k)
		}
	}
	a.pveConsoles[sid] = c
	a.pveMu.Unlock()

	out := map[string]any{"sid": sid, "mode": mode}
	if serial != "" {
		out["serial"] = serial
	}
	if mode == "vga" {
		out["password"] = p.Ticket
	}
	writeJSON(w, http.StatusOK, out)
}

// takeConsole returns a console session once, for the user who opened it.
func (a *API) takeConsole(r *http.Request) *pveConsole {
	sid := r.PathValue("sid")
	ad := adminFrom(r.Context())
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	c := a.pveConsoles[sid]
	if c == nil || ad == nil || c.admin != ad.ID {
		return nil
	}
	delete(a.pveConsoles, sid)
	if time.Now().After(c.expires) {
		return nil
	}
	return c
}

func (a *API) handlePVEConsoleWS(w http.ResponseWriter, r *http.Request) {
	c := a.takeConsole(r)
	if c == nil {
		writeErr(w, http.StatusNotFound, "console session missing or expired")
		return
	}
	dctx, dcancel := context.WithTimeout(r.Context(), 20*time.Second)
	upstream, err := c.cl.DialConsole(dctx, c.node, c.typ, c.vmid, c.proxy)
	dcancel()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer upstream.CloseNow()
	if c.term {
		err = termLogin(r.Context(), upstream, c.proxy)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(1 << 20)

	started := time.Now()
	a.auditAs(r.Context(), c.who, "proxmox.console", c.target, map[string]any{"text": c.term}, true)
	defer func() {
		a.auditAs(context.Background(), c.who, "proxmox.console_close", c.target, map[string]any{"seconds": int(time.Since(started).Seconds())}, true)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
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
