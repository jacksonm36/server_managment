package ops

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker serves a minimal Engine API on a unix socket.
func fakeDocker(t *testing.T) *[]string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	calls := &[]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/version":
			w.Write([]byte(`{"Version":"26.1.5","ApiVersion":"1.45"}`))
		case r.URL.Path == "/containers/json":
			w.Write([]byte(`[{"Id":"0123456789abcdef","Names":["/web"],"Image":"nginx:1.27","State":"running","Status":"Up 2 hours","Created":1,
				"Labels":{"com.docker.compose.project":"blog"},"Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]}]`))
		case r.URL.Path == "/images/json":
			w.Write([]byte(`[{"Id":"sha256:aaaabbbbccccdddd","RepoTags":["nginx:1.27"],"Size":1000,"Created":1}]`))
		case r.URL.Path == "/containers/web/json":
			w.Write([]byte(`{"Config":{"Tty":false}}`))
		case r.URL.Path == "/containers/web/logs":
			var b bytes.Buffer
			for _, line := range []string{"stdout line\n", "stderr line\n"} {
				hdr := make([]byte, 8)
				hdr[0] = 1
				binary.BigEndian.PutUint32(hdr[4:], uint32(len(line)))
				b.Write(hdr)
				b.WriteString(line)
			}
			w.Write(b.Bytes())
		case r.URL.Path == "/containers/missing/stop":
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"No such container: missing"}`))
		default:
			w.WriteHeader(204)
		}
	})
	go http.Serve(l, mux)
	old := dockerSock
	dockerSock = sock
	t.Cleanup(func() { dockerSock = old; l.Close() })
	return calls
}

func TestDockerStatusAndActions(t *testing.T) {
	calls := fakeDocker(t)
	ctx := context.Background()
	res, err := dockerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	cs := m["containers"].([]dContainer)
	if len(cs) != 1 || cs[0].Name != "web" || cs[0].ID != "0123456789ab" || len(cs[0].Ports) != 1 || cs[0].Ports[0] != "8080→80/tcp" {
		t.Fatalf("containers: %+v", cs)
	}
	if apps := m["apps"].([]composeApp); len(apps) != 1 || apps[0].Name != "blog" || apps[0].Running != 1 {
		t.Fatalf("apps: %+v", apps)
	}
	if _, err := dockerContainerAction(ctx, map[string]string{"id": "web", "action": "restart"}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(*calls, ","), "POST /containers/web/restart") {
		t.Fatalf("restart not called: %v", *calls)
	}
	if _, err := dockerContainerAction(ctx, map[string]string{"id": "missing", "action": "stop"}, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("expected docker error, got %v", err)
	}
	for _, bad := range []map[string]string{{"id": "../etc", "action": "stop"}, {"id": "web", "action": "exec"}, {"id": "web/../x", "action": "kill"}} {
		if _, err := dockerContainerAction(ctx, bad, func(string, ...any) {}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	logs, err := dockerLogs(ctx, map[string]string{"id": "web", "lines": "10"})
	if err != nil || logs.(map[string]any)["text"] != "stdout line\nstderr line\n" {
		t.Fatalf("logs: %v %v", logs, err)
	}
}

func TestComposeNames(t *testing.T) {
	for _, ok := range []string{"blog", "my-app_2"} {
		if !projectName.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "Blog", "../x", "a b", "-x", strings.Repeat("a", 64)} {
		if projectName.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
