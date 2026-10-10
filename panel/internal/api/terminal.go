package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// handleTerminal bridges a browser WebSocket to a PTY on the server.
// Browser -> panel: JSON text frames {"type":"input","data":"…"} and
// {"type":"resize","cols":N,"rows":N}. Panel -> browser: binary frames
// with terminal output, and a final JSON {"type":"exit",…}.
// coder/websocket rejects cross-origin upgrades, which (with the session
// cookie being SameSite=Strict) blocks cross-site WebSocket hijacking.
func (a *API) handleTerminal(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	srv, err := a.Store.GetServer(r.Context(), serverID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if !a.Hub.Online(serverID) {
		writeErr(w, http.StatusConflict, "server is offline")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already wrote the response
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)

	who := whoFrom(r)
	sid := store.NewUUID()
	events, unregister := a.Hub.RegisterTerminal(serverID, sid)
	defer unregister()

	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	open := &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_TerminalOpen{TerminalOpen: &agentv1.TerminalOpen{SessionId: sid, Cols: uint32(cols), Rows: uint32(rows)}}}
	if err := a.Hub.Send(serverID, open); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "agent unavailable")
		return
	}
	started := time.Now()
	a.auditAs(r.Context(), who, "terminal.open", srv.Name, map[string]string{"session": sid}, true)
	defer func() {
		_ = a.Hub.Send(serverID, &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_TerminalClose{TerminalClose: &agentv1.TerminalClose{SessionId: sid}}})
		a.auditAs(context.Background(), who, "terminal.close", srv.Name, map[string]any{"session": sid, "seconds": int(time.Since(started).Seconds())}, true)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Browser -> agent.
	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols uint32 `json:"cols"`
				Rows uint32 `json:"rows"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			var msg *agentv1.PanelMessage
			switch m.Type {
			case "input":
				msg = &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_TerminalInput{TerminalInput: &agentv1.TerminalInput{SessionId: sid, Data: []byte(m.Data)}}}
			case "resize":
				msg = &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_TerminalResize{TerminalResize: &agentv1.TerminalResize{SessionId: sid, Cols: m.Cols, Rows: m.Rows}}}
			default:
				continue
			}
			if err := a.Hub.Send(serverID, msg); err != nil {
				return
			}
		}
	}()

	// Agent -> browser.
	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		case e := <-events:
			if e.Exit {
				b, _ := json.Marshal(map[string]any{"type": "exit", "code": e.Code, "error": e.Error})
				_ = conn.Write(ctx, websocket.MessageText, b)
				_ = conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageBinary, e.Data)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}
