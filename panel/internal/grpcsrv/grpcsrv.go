// Package grpcsrv serves the AgentService over mutual TLS. The agent's
// identity is the CommonName of its client certificate (the server ID).
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/panel/internal/hub"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

type Server struct {
	agentv1.UnimplementedAgentServiceServer
	Store *store.Store
	Hub   *hub.Hub
	Log   *slog.Logger
	// OnConnect runs (in its own goroutine) after an agent's Hello; the
	// panel uses it to apply enrollment baselines.
	OnConnect func(serverID string)
}

func (s *Server) GRPCServer(creds credentials.TransportCredentials) *grpc.Server {
	g := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		grpc.MaxRecvMsgSize(4<<20),
	)
	agentv1.RegisterAgentServiceServer(g, s)
	return g
}

func serverIDFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return "", errors.New("no verified client certificate")
	}
	return ti.State.VerifiedChains[0][0].Subject.CommonName, nil
}

func (s *Server) Connect(stream agentv1.AgentService_ConnectServer) error {
	ctx := stream.Context()
	serverID, err := serverIDFromContext(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	if _, err := s.Store.GetServer(ctx, serverID); err != nil {
		// Server was deleted in the panel: refuse the agent.
		return status.Error(codes.PermissionDenied, "unknown server")
	}
	log := s.Log.With("server", serverID)

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}
	if err := s.Store.UpdateServerHello(ctx, &store.Server{
		ID: serverID, Hostname: hello.Hostname, OS: hello.Os, OSVersion: hello.OsVersion,
		OSPretty: hello.OsPretty, Kernel: hello.Kernel, Arch: hello.Arch,
		CPUCount: int(hello.CpuCount), AgentVersion: hello.AgentVersion,
	}); err != nil {
		return status.Error(codes.Internal, "store hello")
	}
	log.Info("agent connected", "hostname", hello.Hostname, "version", hello.AgentVersion)

	if err := s.Store.FailServerJobs(ctx, serverID); err != nil {
		log.Warn("fail stale jobs", "err", err)
	}
	send, detach := s.Hub.Attach(serverID)
	if p, ok := peer.FromContext(ctx); ok {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			s.Hub.SetAddr(serverID, host)
		}
	}
	if s.OnConnect != nil {
		go s.OnConnect(serverID)
	}
	defer func() {
		detach()
		log.Info("agent disconnected")
	}()

	// Writer goroutine: forward queued jobs to the agent.
	errc := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-send:
				if err := stream.Send(msg); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	lastTouch := time.Now()
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		select {
		case err := <-errc:
			return err
		default:
		}
		switch m := msg.Msg.(type) {
		case *agentv1.AgentMessage_Metrics:
			s.Hub.RecordMetric(serverID, hub.MetricFromProto(m.Metrics))
			if time.Since(lastTouch) > time.Minute {
				_ = s.Store.TouchServer(ctx, serverID)
				lastTouch = time.Now()
			}
		case *agentv1.AgentMessage_JobUpdate:
			s.handleJobUpdate(ctx, serverID, m.JobUpdate, log)
		case *agentv1.AgentMessage_TerminalOutput:
			s.Hub.TerminalEvent(serverID, m.TerminalOutput.SessionId, hub.TermEvent{Data: m.TerminalOutput.Data})
		case *agentv1.AgentMessage_TerminalExit:
			e := m.TerminalExit
			s.Hub.TerminalEvent(serverID, e.SessionId, hub.TermEvent{Exit: true, Code: e.Code, Error: e.Error})
		}
	}
}

func (s *Server) handleJobUpdate(ctx context.Context, serverID string, u *agentv1.JobUpdate, log *slog.Logger) {
	job, err := s.Store.GetJob(ctx, u.JobId)
	if err != nil || job.ServerID != serverID {
		log.Warn("job update for unknown job", "job", u.JobId)
		return
	}
	st, finished := "running", false
	switch u.Status {
	case agentv1.JobStatus_JOB_STATUS_SUCCEEDED:
		st, finished = "succeeded", true
	case agentv1.JobStatus_JOB_STATUS_FAILED:
		st, finished = "failed", true
	}
	var logAppend string
	if len(u.LogLines) > 0 {
		logAppend = strings.Join(u.LogLines, "\n") + "\n"
	}
	if err := s.Store.UpdateJob(ctx, u.JobId, st, logAppend, []byte(u.ResultJson), u.Error, finished); err != nil {
		log.Error("update job", "err", err)
		return
	}
	s.Hub.JobChanged(serverID, u.JobId)
}
