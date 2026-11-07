// Package events carries agent events that happen outside a job (a failed
// certificate renewal, a failed scheduled backup) to the panel stream.
package events

import agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"

// C is drained by the panel connection; events raised while the agent is
// offline wait here until the buffer is full, then the oldest are dropped.
var C = make(chan *agentv1.AgentEvent, 64)

// Emit queues an event without ever blocking the caller.
func Emit(kind, subject, message string, ok bool) {
	e := &agentv1.AgentEvent{Kind: kind, Subject: subject, Message: message, Ok: ok}
	for {
		select {
		case C <- e:
			return
		default:
		}
		select {
		case <-C: // drop the oldest
		default:
		}
	}
}
