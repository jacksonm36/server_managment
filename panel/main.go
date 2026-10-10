// Command panel is the servermgr control panel: HTTP API + UI, agent
// enrollment, and the mTLS gRPC endpoint agents connect to.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/jacksonm36/servermgr/panel/internal/api"
	"github.com/jacksonm36/servermgr/panel/internal/grpcsrv"
	"github.com/jacksonm36/servermgr/panel/internal/hub"
	"github.com/jacksonm36/servermgr/panel/internal/pki"
	"github.com/jacksonm36/servermgr/panel/internal/store"
	"github.com/jacksonm36/servermgr/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	var (
		dbURL      = os.Getenv("SM_DATABASE_URL")
		httpAddr   = env("SM_HTTP_ADDR", ":8090")
		grpcAddr   = env("SM_GRPC_ADDR", ":9443")
		publicHost = env("SM_PUBLIC_HOST", "127.0.0.1")
		dataDir    = env("SM_DATA_DIR", "./data")
		binDir     = env("SM_AGENT_BIN_DIR", "./bin")
	)
	if dbURL == "" {
		return errors.New("SM_DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.FailOrphanedJobs(ctx); err != nil {
		return err
	}
	if err := st.FailOrphanedRuns(ctx); err != nil {
		return err
	}
	if err := bootstrapAdmin(ctx, st, log); err != nil {
		return err
	}

	secretKey, err := api.LoadOrCreateKey(dataDir + "/secret.key")
	if err != nil {
		return err
	}

	ca, err := pki.LoadOrCreate(dataDir + "/pki")
	if err != nil {
		return err
	}
	log.Info("internal CA ready", "sha256", ca.Fingerprint())

	hosts := []string{publicHost, "localhost", "127.0.0.1"}
	if hn, err := os.Hostname(); err == nil {
		hosts = append(hosts, hn)
	}
	srvCert, err := ca.ServerCert(hosts)
	if err != nil {
		return err
	}

	h := hub.New()

	// gRPC (agents, mTLS).
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientCAs:    ca.Pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	// The API that handles new connections is built later; agents that
	// connect before then get their baseline on a later connect.
	var onConnect atomic.Pointer[func(string)]
	gs := (&grpcsrv.Server{Store: st, Hub: h, Log: log, OnConnect: func(id string) {
		if f := onConnect.Load(); f != nil {
			(*f)(id)
		}
	}}).GRPCServer(credentials.NewTLS(tlsCfg))
	gl, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	go func() {
		log.Info("gRPC listening", "addr", grpcAddr)
		if err := gs.Serve(gl); err != nil {
			log.Error("grpc serve", "err", err)
			stop()
		}
	}()

	// HTTP (UI + API + enrollment).
	// SM_WEB_DIR serves the UI straight from disk, so a `npm run build`
	// is picked up without restarting the panel.
	var webFS fs.FS
	if dir := os.Getenv("SM_WEB_DIR"); dir != "" {
		webFS = os.DirFS(dir)
		log.Info("serving UI from disk", "dir", dir)
	} else if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			webFS = sub
		}
	}
	_, grpcPort, _ := net.SplitHostPort(grpcAddr)
	if err := st.FailStaleProvisions(ctx); err != nil {
		log.Warn("proxmox: mark interrupted provisions", "err", err)
	}
	if err := st.FailStaleTofuRuns(ctx); err != nil {
		log.Warn("tofu: mark interrupted runs", "err", err)
	}
	if err := st.FailStaleAnsibleRuns(ctx); err != nil {
		log.Warn("ansible: mark interrupted runs", "err", err)
	}
	a := &api.API{
		Cfg: api.Config{
			PublicURL:    "http://" + net.JoinHostPort(publicHost, portOf(httpAddr)),
			GRPCAddr:     net.JoinHostPort(publicHost, grpcPort),
			AgentBinDir:  binDir,
			ProgressFile: env("SM_PROGRESS_FILE", "./progress.json"),
			DataDir:      dataDir,
		},
		Store: st, Hub: h, CA: ca, Log: log, Web: webFS, SecretKey: secretKey, Ctx: ctx,
	}
	applyBaseline := a.ApplyBaseline
	onConnect.Store(&applyBaseline)
	go a.RunScheduler(ctx)
	go a.RunAlerts(ctx)

	handler := a.Handler() // one instance, so both listeners share rate limits
	hs := &http.Server{
		Addr:              httpAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// HTTPS for the UI, with a certificate from the panel CA (download it from
	// /ca.crt to trust it). Plain HTTP stays up for agent install/enrollment,
	// which pins the CA fingerprint itself. SM_HTTPS_ADDR=off disables it.
	var hss *http.Server
	if httpsAddr := env("SM_HTTPS_ADDR", ":8443"); httpsAddr != "off" {
		hss = &http.Server{
			Addr:              httpsAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{srvCert}, MinVersion: tls.VersionTLS12},
		}
		go func() {
			log.Info("HTTPS listening", "addr", httpsAddr)
			if err := hss.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("https serve", "err", err)
			}
		}()
	}
	go func() {
		log.Info("HTTP listening", "addr", httpAddr, "public_url", a.Cfg.PublicURL)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http serve", "err", err)
			stop()
		}
	}()

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := st.PurgeExpired(ctx); err != nil {
					log.Warn("purge expired", "err", err)
				}
			}
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutCtx)
	if hss != nil {
		_ = hss.Shutdown(shutCtx)
	}
	gs.Stop()
	return nil
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimPrefix(addr, ":")
	}
	return p
}

// bootstrapAdmin creates the first admin from SM_ADMIN_USER/SM_ADMIN_PASSWORD
// when no admin exists yet. The password is only read on that first start.
func bootstrapAdmin(ctx context.Context, st *store.Store, log *slog.Logger) error {
	n, err := st.AdminCount(ctx)
	if err != nil || n > 0 {
		return err
	}
	user := env("SM_ADMIN_USER", "admin")
	pw := os.Getenv("SM_ADMIN_PASSWORD")
	if len(pw) < 12 {
		return errors.New("no admin exists: set SM_ADMIN_PASSWORD (min 12 chars) for the first start")
	}
	hash, err := api.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := st.CreateAdmin(ctx, user, hash); err != nil {
		return err
	}
	log.Info("created initial admin", "username", user)
	return nil
}
