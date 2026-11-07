package api

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

// eventNames turns agent event kinds into words for the audit log and
// notifications.
var eventNames = map[string]string{
	"tls.renew":        "Certificate renewal",
	"backup.scheduled": "Scheduled backup",
	"agent.cert_renew": "Agent certificate renewal",
}

// HandleAgentEvent records an event an agent reported outside a job. Failed
// ones also go to every channel that takes events.
func (a *API) HandleAgentEvent(serverID string, e *agentv1.AgentEvent) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 30*time.Second)
	defer cancel()
	name := serverID
	if s, err := a.Store.GetServer(ctx, serverID); err == nil {
		name = s.Name
	}
	target := name
	if e.Subject != "" {
		target = name + " · " + truncate(e.Subject, 200)
	}
	a.auditAs(ctx, Who{Actor: "agent"}, "agent."+truncate(e.Kind, 40), target, map[string]string{"message": truncate(e.Message, 1000)}, e.Ok)
	if e.Ok {
		return
	}
	what := eventNames[e.Kind]
	if what == "" {
		what = e.Kind
	}
	a.notifyEvent(ctx, Message{
		Title:  fmt.Sprintf("[FAILED] %s on %s: %s", what, target, truncate(e.Message, 500)),
		Firing: true, Server: name, Rule: what,
	})
}
