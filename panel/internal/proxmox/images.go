package proxmox

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Image is an official cloud image that can become a VM template.
type Image struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	Vendor    string `yaml:"vendor" json:"vendor"`
	User      string `yaml:"user" json:"user"`
	URL       string `yaml:"url" json:"url"`
	Checksums string `yaml:"checksums" json:"checksums,omitempty"`
}

//go:embed images.yaml
var imagesYAML []byte

// Images is the built-in image list.
var Images = func() []Image {
	var list []Image
	if err := yaml.Unmarshal(imagesYAML, &list); err != nil {
		panic("proxmox/images.yaml: " + err.Error())
	}
	return list
}()

func ImageByID(id string) *Image {
	for i := range Images {
		if Images[i].ID == id {
			return &Images[i]
		}
	}
	return nil
}

// ImportName is the file name the image gets in a storage's import
// content. Proxmox only imports .qcow2, .raw and .vmdk names; vendors'
// ".img" files are qcow2.
func (img *Image) ImportName() string {
	return "servermgr-" + img.ID + ".qcow2"
}

var (
	bsdSum = regexp.MustCompile(`^(SHA256|SHA512) \((.+)\) = ([0-9a-fA-F]+)$`)
	gnuSum = regexp.MustCompile(`^([0-9a-fA-F]{64}|[0-9a-fA-F]{128})\s+\*?(\S+)$`)
)

// FindChecksum picks the image's line out of a checksum file, in GNU
// ("<hash>  <file>") or BSD ("SHA256 (<file>) = <hash>") style. It
// returns the algorithm Proxmox names (sha256 or sha512) and the hash.
func FindChecksum(sums io.Reader, file string) (algo, sum string, ok bool) {
	sc := bufio.NewScanner(sums)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		var name, hash string
		if m := bsdSum.FindStringSubmatch(line); m != nil {
			name, hash = m[2], m[3]
		} else if m := gnuSum.FindStringSubmatch(line); m != nil {
			name, hash = m[2], m[1]
		} else {
			continue
		}
		if path.Base(name) != file {
			continue
		}
		switch len(hash) {
		case 64:
			return "sha256", strings.ToLower(hash), true
		case 128:
			return "sha512", strings.ToLower(hash), true
		}
	}
	return "", "", false
}

// FetchChecksum downloads the image's checksum file and finds its hash.
func (img *Image) FetchChecksum(ctx context.Context) (algo, sum string, err error) {
	if img.Checksums == "" {
		return "", "", fmt.Errorf("the vendor publishes no checksum file")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, img.Checksums, nil)
	if err != nil {
		return "", "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s: %s", img.Checksums, res.Status)
	}
	algo, sum, ok := FindChecksum(io.LimitReader(res.Body, 4<<20), path.Base(img.URL))
	if !ok {
		return "", "", fmt.Errorf("%s does not list %s", img.Checksums, path.Base(img.URL))
	}
	return algo, sum, nil
}
