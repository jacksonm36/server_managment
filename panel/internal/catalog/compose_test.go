package catalog

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestComposeTemplates parses every compose template with sample values and
// checks its shape, so a mis-indented line fails here, not on a server.
func TestComposeTemplates(t *testing.T) {
	serviceKeys := map[string]bool{"image": true, "restart": true, "ports": true, "environment": true, "volumes": true,
		"depends_on": true, "healthcheck": true, "command": true, "user": true, "extra_hosts": true}
	r := strings.NewReplacer("${port}", "20001", "${domain}", "a.test", "${site_url}", "https://a.test", "${title}", "T",
		"${secret_16}", "s16", "${secret_32}", "s32", "${secret_64}", "s64",
		"${admin_user}", "admin", "${admin_email}", "a@b.test", "${admin_password}", "pw")
	for _, a := range List() {
		if a.Kind != "compose" {
			continue
		}
		y := r.Replace(a.Compose)
		if strings.Contains(y, "${") {
			t.Errorf("%s: unknown variable left in compose", a.ID)
		}
		var doc struct {
			Services map[string]map[string]any `yaml:"services"`
			Volumes  map[string]any            `yaml:"volumes"`
		}
		if err := yaml.Unmarshal([]byte(y), &doc); err != nil {
			t.Errorf("%s: %v", a.ID, err)
			continue
		}
		published := 0
		for name, svc := range doc.Services {
			for k := range svc {
				if !serviceKeys[k] {
					t.Errorf("%s/%s: unexpected service key %q (indentation?)", a.ID, name, k)
				}
			}
			if img, _ := svc["image"].(string); img == "" || strings.HasSuffix(img, ":latest") || !strings.Contains(img, ":") {
				t.Errorf("%s/%s: image must be pinned to a version, got %q", a.ID, name, img)
			}
			if env, ok := svc["environment"]; ok {
				if _, ok := env.(map[string]any); !ok {
					t.Errorf("%s/%s: environment must be a map", a.ID, name)
				}
			}
			ports, _ := svc["ports"].([]any)
			for _, p := range ports {
				if !strings.HasPrefix(p.(string), "127.0.0.1:20001:") {
					t.Errorf("%s/%s: ports must be 127.0.0.1:${port}:N, got %v", a.ID, name, p)
				}
				published++
			}
		}
		if published != 1 {
			t.Errorf("%s: exactly one published port expected, got %d", a.ID, published)
		}
	}
}
