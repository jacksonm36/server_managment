package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// dockerSock is a variable so tests can point it at a fake daemon.
var dockerSock = "/var/run/docker.sock"

// composeRoot holds one directory per compose app: <name>/compose.yaml.
var composeRoot = "/srv/compose"

var (
	containerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	imageRef     = regexp.MustCompile(`^[a-z0-9]+([._/:@-][a-zA-Z0-9]+)*$`)
	projectName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

func dockerClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", dockerSock)
		}},
	}
}

type dockerError struct{ Message string }

// dockerDo calls the Engine API; out (if non-nil) receives the JSON body.
func dockerDo(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := dockerClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var de dockerError
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &de) == nil && de.Message != "" {
			return errors.New(de.Message)
		}
		return fmt.Errorf("docker: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

type dContainer struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
	Created int64    `json:"created"`
	Ports   []string `json:"ports"`
	Project string   `json:"project,omitempty"`
}

type dImage struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
}

type composeApp struct {
	Name    string `json:"name"`
	Running int    `json:"running"`
	Total   int    `json:"total"`
}

func dockerStatus(ctx context.Context) (any, error) {
	if _, err := os.Stat(dockerSock); err != nil {
		_, cliErr := exec.LookPath("docker")
		return map[string]any{"installed": cliErr == nil, "running": false}, nil
	}
	var ver struct{ Version, APIVersion string }
	if err := dockerDo(ctx, "GET", "/version", nil, &ver); err != nil {
		return map[string]any{"installed": true, "running": false, "error": err.Error()}, nil
	}
	var raw []struct {
		Id, Image, State, Status string
		Names                    []string
		Created                  int64
		Labels                   map[string]string
		Ports                    []struct {
			IP          string
			PrivatePort int
			PublicPort  int
			Type        string
		}
	}
	if err := dockerDo(ctx, "GET", "/containers/json?all=1", nil, &raw); err != nil {
		return nil, err
	}
	containers := []dContainer{}
	apps := map[string]*composeApp{}
	for _, c := range raw {
		dc := dContainer{ID: c.Id[:12], Image: c.Image, State: c.State, Status: c.Status, Created: c.Created, Ports: []string{}, Project: c.Labels["com.docker.compose.project"]}
		if len(c.Names) > 0 {
			dc.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		seen := map[string]bool{}
		for _, p := range c.Ports {
			s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			if p.PublicPort > 0 {
				s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			}
			if !seen[s] {
				seen[s] = true
				dc.Ports = append(dc.Ports, s)
			}
		}
		containers = append(containers, dc)
		if dc.Project != "" {
			a := apps[dc.Project]
			if a == nil {
				a = &composeApp{Name: dc.Project}
				apps[dc.Project] = a
			}
			a.Total++
			if dc.State == "running" {
				a.Running++
			}
		}
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })

	var rawImgs []struct {
		Id       string
		RepoTags []string
		Size     int64
		Created  int64
	}
	if err := dockerDo(ctx, "GET", "/images/json", nil, &rawImgs); err != nil {
		return nil, err
	}
	images := []dImage{}
	for _, im := range rawImgs {
		tags := im.RepoTags
		if tags == nil {
			tags = []string{}
		}
		images = append(images, dImage{ID: strings.TrimPrefix(im.Id, "sha256:")[:12], Tags: tags, Size: im.Size, Created: im.Created})
	}

	// Compose apps managed here, including ones with no containers yet.
	if ents, err := os.ReadDir(composeRoot); err == nil {
		for _, e := range ents {
			if e.IsDir() && projectName.MatchString(e.Name()) && apps[e.Name()] == nil {
				apps[e.Name()] = &composeApp{Name: e.Name()}
			}
		}
	}
	appList := []composeApp{}
	for _, a := range apps {
		appList = append(appList, *a)
	}
	sort.Slice(appList, func(i, j int) bool { return appList[i].Name < appList[j].Name })

	return map[string]any{"installed": true, "running": true, "version": ver.Version, "containers": containers, "images": images, "apps": appList}, nil
}

func dockerInstall(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := aptInstall(ctx, logf, "docker.io", "docker-compose"); err != nil {
		return nil, err
	}
	if err := run(ctx, logf, "systemctl", "enable", "--now", "docker"); err != nil {
		return nil, err
	}
	return dockerStatus(ctx)
}

func dockerContainerAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	id := p["id"]
	if !containerRef.MatchString(id) {
		return nil, errors.New("invalid container id")
	}
	var err error
	switch a := p["action"]; a {
	case "start", "stop", "restart", "kill":
		err = dockerDo(ctx, "POST", "/containers/"+id+"/"+a, nil, nil)
	case "remove":
		err = dockerDo(ctx, "DELETE", "/containers/"+id+"?force=1&v=0", nil, nil)
	default:
		return nil, errors.New("action must be start, stop, restart, kill or remove")
	}
	if err != nil {
		return nil, err
	}
	logf("%s %s: ok", p["action"], id)
	return dockerStatus(ctx)
}

// demux splits Docker's multiplexed log stream (8-byte frame headers).
func demux(r io.Reader) string {
	var b strings.Builder
	br := bufio.NewReader(r)
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(&b, br, int64(n)); err != nil {
			break
		}
	}
	return b.String()
}

func dockerLogs(ctx context.Context, p map[string]string) (any, error) {
	id := p["id"]
	if !containerRef.MatchString(id) {
		return nil, errors.New("invalid container id")
	}
	var info struct{ Config struct{ Tty bool } }
	if err := dockerDo(ctx, "GET", "/containers/"+id+"/json", nil, &info); err != nil {
		return nil, err
	}
	tail := clampLines(p["lines"], 300)
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://docker/containers/%s/logs?stdout=1&stderr=1&timestamps=1&tail=%d", id, tail), nil)
	resp, err := dockerClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 4<<20)
	var text string
	if info.Config.Tty {
		b, _ := io.ReadAll(body)
		text = string(b)
	} else {
		text = demux(body)
	}
	return map[string]any{"text": text}, nil
}

func dockerImageRemove(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	id := p["id"]
	if !containerRef.MatchString(id) && !imageRef.MatchString(id) {
		return nil, errors.New("invalid image")
	}
	if err := dockerDo(ctx, "DELETE", "/images/"+url.PathEscape(id), nil, nil); err != nil {
		return nil, err
	}
	logf("removed image %s", id)
	return dockerStatus(ctx)
}

func dockerPrune(ctx context.Context, logf func(string, ...any)) (any, error) {
	var res struct{ SpaceReclaimed int64 }
	if err := dockerDo(ctx, "POST", "/containers/prune", nil, nil); err != nil {
		return nil, err
	}
	if err := dockerDo(ctx, "POST", `/images/prune?filters=`+url.QueryEscape(`{"dangling":["true"]}`), nil, &res); err != nil {
		return nil, err
	}
	logf("pruned stopped containers and dangling images (%d bytes reclaimed)", res.SpaceReclaimed)
	return dockerStatus(ctx)
}

// ---- compose apps ----

func composeFile(name string) string { return filepath.Join(composeRoot, name, "compose.yaml") }

func composeRead(p map[string]string) (any, error) {
	if !projectName.MatchString(p["name"]) {
		return nil, errors.New("invalid app name")
	}
	b, err := os.ReadFile(composeFile(p["name"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": p["name"], "yaml": string(b)}, nil
}

func compose(ctx context.Context, logf func(string, ...any), name string, args ...string) error {
	full := append([]string{"compose", "-p", name, "-f", composeFile(name)}, args...)
	logf("$ docker %s", strings.Join(full, " "))
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = filepath.Dir(composeFile(name))
	return streamCmd(cmd, logf)
}

// composeSave writes the app's compose.yaml after `docker compose config`
// validates it, then (optionally) deploys it.
func composeSave(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if !projectName.MatchString(name) {
		return nil, errors.New("app name: lowercase letters, digits, - and _ (max 63)")
	}
	yaml := p["yaml"]
	if strings.TrimSpace(yaml) == "" || len(yaml) > 256<<10 {
		return nil, errors.New("compose file is empty or too large")
	}
	dir := filepath.Dir(composeFile(name))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	prev, prevErr := os.ReadFile(composeFile(name))
	if err := writeFileAtomic(composeFile(name), []byte(yaml), 0o640); err != nil {
		return nil, err
	}
	check := exec.CommandContext(ctx, "docker", "compose", "-p", name, "-f", composeFile(name), "config", "--quiet")
	check.Dir = dir
	if out, err := check.CombinedOutput(); err != nil {
		if prevErr == nil {
			_ = writeFileAtomic(composeFile(name), prev, 0o640)
		} else {
			_ = os.RemoveAll(dir)
		}
		return nil, fmt.Errorf("invalid compose file, nothing changed: %s", strings.TrimSpace(string(out)))
	}
	logf("saved %s", composeFile(name))
	if p["deploy"] == "true" {
		if err := compose(ctx, logf, name, "up", "-d", "--remove-orphans"); err != nil {
			return nil, err
		}
	}
	return dockerStatus(ctx)
}

func composeAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if !projectName.MatchString(name) {
		return nil, errors.New("invalid app name")
	}
	if _, err := os.Stat(composeFile(name)); err != nil {
		return nil, fmt.Errorf("app %s has no compose file on this server", name)
	}
	var err error
	switch p["action"] {
	case "up":
		err = compose(ctx, logf, name, "up", "-d", "--remove-orphans")
	case "down":
		err = compose(ctx, logf, name, "down")
	case "restart":
		err = compose(ctx, logf, name, "restart")
	case "pull":
		if err = compose(ctx, logf, name, "pull"); err == nil {
			err = compose(ctx, logf, name, "up", "-d", "--remove-orphans")
		}
	case "delete":
		if err = compose(ctx, logf, name, "down", "--remove-orphans"); err == nil {
			err = os.RemoveAll(filepath.Dir(composeFile(name)))
			logf("removed %s (named volumes are kept)", filepath.Dir(composeFile(name)))
		}
	default:
		return nil, errors.New("action must be up, down, restart, pull or delete")
	}
	if err != nil {
		return nil, err
	}
	return dockerStatus(ctx)
}
