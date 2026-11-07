package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/jacksonm36/servermgr/agent/internal/events"
	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
	"github.com/jacksonm36/servermgr/agent/internal/term"
	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

const metricsInterval = 5 * time.Second

type client struct {
	addr  string
	tls   *tls.Config
	certs *certStore
	log   *slog.Logger
	sys   *sysinfo.Collector
}

// renewCheckInterval is how often a connected agent checks whether its
// client certificate is due for renewal.
const renewCheckInterval = 12 * time.Hour

// loop keeps a stream to the panel open, reconnecting with jittered
// exponential backoff (1 s .. 60 s).
func (c *client) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		c.log.Warn("disconnected from panel", "err", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (c *client) session(ctx context.Context) error {
	conn, err := grpc.NewClient(c.addr,
		grpc.WithTransportCredentials(credentials.NewTLS(c.tls)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := agentv1.NewAgentServiceClient(conn).Connect(ctx)
	if err != nil {
		return err
	}

	// All sends go through one goroutine; gRPC streams are not safe for
	// concurrent Send calls.
	out := make(chan *agentv1.AgentMessage, 32)
	sendErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-out:
				if err := stream.Send(m); err != nil {
					sendErr <- err
					cancel()
					return
				}
			}
		}
	}()

	out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_Hello{Hello: c.sys.Hello(version)}}
	c.log.Info("connected to panel", "addr", c.addr)

	// Metrics publisher.
	go func() {
		t := time.NewTicker(metricsInterval)
		defer t.Stop()
		for {
			if m, err := c.sys.Metrics(); err == nil {
				select {
				case out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_Metrics{Metrics: m}}:
				case <-ctx.Done():
					return
				}
			} else {
				c.log.Warn("collect metrics", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// Events raised outside jobs (certificate renewals, scheduled backups).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-events.C:
				select {
				case out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_Event{Event: e}}:
				case <-ctx.Done():
					events.Emit(e.Kind, e.Subject, e.Message, e.Ok) // keep it for the next connection
					return
				}
			}
		}
	}()

	// Client certificate renewal.
	go func() {
		t := time.NewTicker(renewCheckInterval)
		defer t.Stop()
		for {
			if c.certs != nil && renewDue(c.certs.leaf(), time.Now()) {
				if csr, err := c.certs.newCSR(); err != nil {
					c.log.Error("certificate renewal: CSR", "err", err)
				} else {
					c.log.Info("requesting a new client certificate", "expires", c.certs.leaf().NotAfter)
					select {
					case out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_CertRenew{CertRenew: &agentv1.CertRenewRequest{CsrPem: csr}}}:
					case <-ctx.Done():
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// Running jobs, so the panel can cancel them.
	var jobsMu sync.Mutex
	jobs := map[string]context.CancelFunc{}

	terms := term.NewManager(func(m *agentv1.AgentMessage) {
		select {
		case out <- m:
		case <-ctx.Done():
		}
	})
	defer terms.Close()

	for {
		msg, err := stream.Recv()
		if err != nil {
			select {
			case e := <-sendErr:
				return e
			default:
				return err
			}
		}
		switch {
		case msg.GetTerminalOpen() != nil:
			terms.Open(msg.GetTerminalOpen())
			continue
		case msg.GetTerminalInput() != nil:
			terms.Input(msg.GetTerminalInput())
			continue
		case msg.GetTerminalResize() != nil:
			terms.Resize(msg.GetTerminalResize())
			continue
		case msg.GetTerminalClose() != nil:
			terms.Kill(msg.GetTerminalClose().SessionId)
			continue
		case msg.GetCertRenew() != nil:
			r := msg.GetCertRenew()
			if r.Error != "" {
				c.log.Error("certificate renewal refused by the panel", "err", r.Error)
			} else if err := c.certs.apply(r.CertPem); err != nil {
				c.log.Error("certificate renewal", "err", err)
				events.Emit("agent.cert_renew", "", err.Error(), false)
			} else {
				c.log.Info("client certificate renewed", "expires", c.certs.leaf().NotAfter)
				events.Emit("agent.cert_renew", "", "renewed until "+c.certs.leaf().NotAfter.UTC().Format(time.DateOnly), true)
			}
			continue
		case msg.GetJobCancel() != nil:
			jobsMu.Lock()
			if cancelJob := jobs[msg.GetJobCancel().JobId]; cancelJob != nil {
				cancelJob()
			}
			jobsMu.Unlock()
			continue
		}
		if job := msg.GetJob(); job != nil {
			jctx, jcancel := context.WithCancelCause(ctx)
			jobsMu.Lock()
			jobs[job.Id] = func() { jcancel(errJobCancelled) }
			jobsMu.Unlock()
			go func() {
				defer func() {
					jobsMu.Lock()
					delete(jobs, job.Id)
					jobsMu.Unlock()
					jcancel(nil)
				}()
				runJob(jctx, job, func(u *agentv1.JobUpdate) {
					select {
					case out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_JobUpdate{JobUpdate: u}}:
					case <-ctx.Done():
					}
				})
			}()
		}
	}
}

// errJobCancelled is the cause set when the panel cancels a job.
var errJobCancelled = jobCancelled{}

type jobCancelled struct{}

func (jobCancelled) Error() string { return "cancelled from the panel" }
