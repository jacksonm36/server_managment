package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/jacksonm36/servermgr/agent/internal/ops"
	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

const metricsInterval = 5 * time.Second

type client struct {
	addr string
	tls  *tls.Config
	log  *slog.Logger
	sys  *sysinfo.Collector
}

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
		if job := msg.GetJob(); job != nil {
			go ops.Run(ctx, job, func(u *agentv1.JobUpdate) {
				select {
				case out <- &agentv1.AgentMessage{Msg: &agentv1.AgentMessage_JobUpdate{JobUpdate: u}}:
				case <-ctx.Done():
				}
			})
		}
	}
}
