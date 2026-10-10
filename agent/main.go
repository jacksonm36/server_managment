// Command servermgr-agent runs on each managed server. It enrolls once with a
// join token, then keeps an outbound mTLS gRPC stream to the panel.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jacksonm36/servermgr/agent/internal/ops"
	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
)

var version = "0.1.0-dev"

const defaultStateDir = "/etc/servermgr-agent"

type state struct {
	ServerID string `json:"server_id"`
	GRPCAddr string `json:"grpc_addr"`
	PanelURL string `json:"panel_url"`
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:], log)
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: servermgr-agent enroll --panel URL --token TOKEN --ca-sha256 HEX | run | version")
	os.Exit(2)
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	panel := fs.String("panel", "", "panel base URL, e.g. http://10.0.1.240:8090")
	token := fs.String("token", "", "one-time join token")
	caSHA := fs.String("ca-sha256", "", "expected SHA-256 of the panel CA certificate")
	dir := fs.String("state-dir", defaultStateDir, "state directory")
	_ = fs.Parse(args)
	if *panel == "" || *token == "" || *caSHA == "" {
		return errors.New("--panel, --token and --ca-sha256 are required")
	}

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}, key)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{
		"token":    *token,
		"csr_pem":  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
		"hostname": hostname,
	})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(strings.TrimRight(*panel, "/")+"/api/v1/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enroll failed: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ServerID string `json:"server_id"`
		CertPEM  string `json:"cert_pem"`
		CAPEM    string `json:"ca_pem"`
		GRPCAddr string `json:"grpc_addr"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}

	// Verify the CA we were handed matches the fingerprint from the install
	// command, so a tampered enrollment response cannot plant a rogue CA.
	b, _ := pem.Decode([]byte(out.CAPEM))
	if b == nil {
		return errors.New("enroll: invalid CA PEM")
	}
	sum := sha256.Sum256(b.Bytes)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), *caSHA) {
		return errors.New("enroll: panel CA fingerprint mismatch, refusing to trust it")
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"agent.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		"agent.crt": []byte(out.CertPEM),
		"ca.crt":    []byte(out.CAPEM),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(*dir, name), data, 0o600); err != nil {
			return err
		}
	}
	st, _ := json.MarshalIndent(state{ServerID: out.ServerID, GRPCAddr: out.GRPCAddr, PanelURL: *panel}, "", "  ")
	if err := os.WriteFile(filepath.Join(*dir, "state.json"), st, 0o600); err != nil {
		return err
	}
	fmt.Printf("enrolled as server %s (panel %s)\n", out.ServerID, out.GRPCAddr)
	return nil
}

func cmdRun(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("state-dir", defaultStateDir, "state directory")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(filepath.Join(*dir, "state.json"))
	if err != nil {
		return fmt.Errorf("not enrolled (%w); run `servermgr-agent enroll` first", err)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(*dir, "agent.crt"), filepath.Join(*dir, "agent.key"))
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(filepath.Join(*dir, "ca.crt"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return errors.New("invalid ca.crt")
	}
	host := st.GRPCAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = strings.Trim(host[:i], "[]")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   host,
		MinVersion:   tls.VersionTLS13,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ops.PanelURL = st.PanelURL
	ops.StartRenewer(ctx, log)
	ops.StartBackups(ctx, log)
	c := &client{addr: st.GRPCAddr, tls: tlsCfg, log: log.With("server", st.ServerID), sys: sysinfo.New()}
	c.loop(ctx)
	return nil
}
