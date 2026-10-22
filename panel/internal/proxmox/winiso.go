package proxmox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Retail Windows 10/11 ISOs straight from Microsoft.
//
// The multi-edition ISO (Home, Pro, Education…) in any of Microsoft's
// languages comes from the API behind microsoft.com/software-download. Its
// link is valid for 24 hours, so nothing is stored: each request asks
// Microsoft for a fresh link and hands it to Proxmox at once. Microsoft
// refuses some networks (error 715-123130); the evaluation ISOs in
// isos.yaml are the fallback.
//
// Every request must stay on https and on a microsoft.com host.

// WinRetailIndex is Pro's image in the multi-edition install.wim.
const WinRetailIndex = 6

// WinISO is a resolved download.
type WinISO struct {
	URL  string `json:"-"`
	File string `json:"file"`
}

var (
	msWWW     = "https://www.microsoft.com"
	msTags    = "https://vlscppe.microsoft.com/tags"
	msOVDF    = "https://ov-df.microsoft.com/"
	msHTTP    = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	msHostOK  = func(host string) bool { return host == "microsoft.com" || strings.HasSuffix(host, ".microsoft.com") }
	msBrowser = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
)

// Constants of Microsoft's download page; they have not changed in years.
const (
	msOrgID    = "y6jn8c31"
	msProfile  = "606624d44113"
	msInstance = "560dc9f3-1aa5-4a2f-b63c-9e18f8d0e175"
)

var retailPages = map[string]struct{ page, option string }{
	"win11": {"/en-us/software-download/windows11", `Windows 11 \(multi-edition ISO for x64`},
	"win10": {"/en-us/software-download/windows10ISO", `Windows 10 \(multi-edition ISO`},
}

// msGet fetches a Microsoft URL without following redirects.
func msGet(ctx context.Context, u string, hdr map[string]string, max int64) (*http.Response, []byte, error) {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" || !msHostOK(pu.Hostname()) {
		return nil, nil, fmt.Errorf("refusing %s: not an https microsoft.com link", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", msBrowser)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := msHTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("could not reach %s: %v", pu.Host, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, max))
	return res, b, err
}

func msPage(ctx context.Context, p string) (string, error) {
	res, b, err := msGet(ctx, msWWW+p, nil, 4<<20)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("microsoft.com answered %s for %s", res.Status, p)
	}
	return string(b), nil
}

// retailLang maps a Windows locale to the language names of Microsoft's
// download API, in order of preference.
var retailLang = map[string][]string{
	"ar-SA": {"Arabic"}, "pt-BR": {"Brazilian Portuguese"}, "bg-BG": {"Bulgarian"},
	"zh-CN": {"Chinese (Simplified)"}, "zh-TW": {"Chinese (Traditional)"}, "hr-HR": {"Croatian"},
	"cs-CZ": {"Czech"}, "da-DK": {"Danish"}, "nl-NL": {"Dutch"}, "en-US": {"English"},
	"en-GB": {"English (United Kingdom)", "English International"}, "et-EE": {"Estonian"},
	"fi-FI": {"Finnish"}, "fr-FR": {"French"}, "fr-CA": {"French Canadian"}, "de-DE": {"German"},
	"el-GR": {"Greek"}, "he-IL": {"Hebrew"}, "hu-HU": {"Hungarian"}, "it-IT": {"Italian"},
	"ja-JP": {"Japanese"}, "ko-KR": {"Korean"}, "lv-LV": {"Latvian"}, "lt-LT": {"Lithuanian"},
	"nb-NO": {"Norwegian"}, "pl-PL": {"Polish"}, "pt-PT": {"Portuguese"}, "ro-RO": {"Romanian"},
	"ru-RU": {"Russian"}, "sk-SK": {"Slovak"}, "sl-SI": {"Slovenian"}, "es-ES": {"Spanish"},
	"es-MX": {"Spanish (Mexico)"}, "sv-SE": {"Swedish"}, "th-TH": {"Thai"}, "tr-TR": {"Turkish"},
	"uk-UA": {"Ukrainian"},
}

var (
	mdtWRe     = regexp.MustCompile(`[?&]w=([A-F0-9]+)`)
	mdtTicksRe = regexp.MustCompile(`rticks="\+?(\d+)`)
)

func hostOf(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return pu.Hostname()
	}
	return u
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type msErrors []struct {
	Value string `json:"Value"`
	Type  int    `json:"Type"`
}

// err reports Microsoft's errors. unreached are the check hosts this
// server could not reach, the usual reason Microsoft refuses a session.
func (e msErrors) err(unreached []string) error {
	if len(e) == 0 {
		return nil
	}
	if e[0].Type == 9 || strings.Contains(e[0].Value, "Sentinel") {
		if len(unreached) > 0 {
			return fmt.Errorf("Microsoft refused the download because this server cannot reach %s. DNS filters (Pi-hole, AdGuard, router blocklists) often block these as tracking: allow vlscppe.microsoft.com, ov-df.microsoft.com, h-microsoft.online-metrix.net and fpt.dfp.microsoft.com, or pick the Enterprise evaluation", strings.Join(unreached, " and "))
		}
		return errors.New("Microsoft refused the download from this network (error 715-123130); try the evaluation ISO or again later")
	}
	return fmt.Errorf("Microsoft: %s", e[0].Value)
}

// retailISO asks Microsoft's download API for the multi-edition ISO.
func retailISO(ctx context.Context, edition, locale string) (string, error) {
	rp, ok := retailPages[edition]
	if !ok {
		return "", errors.New("no retail ISO for this edition")
	}
	langs := retailLang[locale]
	if langs == nil {
		return "", fmt.Errorf("Microsoft has no %s Windows ISO; pick another language", locale)
	}
	page, err := msPage(ctx, rp.page)
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`<option value="(\d+)">` + rp.option).FindStringSubmatch(page)
	if m == nil {
		return "", errors.New("the Microsoft download page no longer lists the ISO")
	}
	sid := newSessionID()
	// Microsoft only serves sessions that went through its page scripts.
	// DNS filters often block their hosts; the steps are tried anyway, and
	// if Microsoft then refuses, the error names the hosts to allow.
	var unreached []string
	if _, _, err := msGet(ctx, msTags+"?org_id="+msOrgID+"&session_id="+sid, nil, 1<<20); err != nil {
		unreached = append(unreached, hostOf(msTags))
	}
	if _, js, err := msGet(ctx, msOVDF+"mdt.js?instanceId="+msInstance+"&PageId=si&session_id="+sid, nil, 1<<20); err != nil {
		unreached = append(unreached, hostOf(msOVDF))
	} else if w, ticks := mdtWRe.FindSubmatch(js), mdtTicksRe.FindSubmatch(js); w != nil && ticks != nil {
		q := url.Values{"session_id": {sid}, "CustomerId": {msInstance}, "PageId": {"si"}, "w": {string(w[1])},
			"mdt": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "rticks": {string(ticks[1])}}
		if _, _, err := msGet(ctx, msOVDF+"?"+q.Encode(), nil, 1<<20); err != nil {
			unreached = append(unreached, hostOf(msOVDF))
		}
	}
	api := msWWW + "/software-download-connector/api/"
	var skus struct {
		Errors msErrors `json:"Errors"`
		Skus   []struct {
			ID       string `json:"Id"`
			Language string `json:"Language"`
		} `json:"Skus"`
	}
	q := url.Values{"profile": {msProfile}, "productEditionId": {m[1]}, "SKU": {"undefined"}, "friendlyFileName": {"undefined"}, "Locale": {"en-US"}, "sessionID": {sid}}
	if err := msJSON(ctx, api+"getskuinformationbyproductedition?"+q.Encode(), nil, &skus); err != nil {
		return "", err
	}
	if err := skus.Errors.err(unreached); err != nil {
		return "", err
	}
	sku := ""
	for _, l := range langs {
		for _, s := range skus.Skus {
			if sku == "" && s.Language == l {
				sku = s.ID
			}
		}
	}
	if sku == "" {
		return "", fmt.Errorf("Microsoft has no %s Windows ISO; pick another language", locale)
	}
	var links struct {
		Errors  msErrors `json:"Errors"`
		Options []struct {
			URI  string `json:"Uri"`
			Type int    `json:"DownloadType"`
		} `json:"ProductDownloadOptions"`
	}
	q = url.Values{"profile": {msProfile}, "productEditionId": {"undefined"}, "SKU": {sku}, "friendlyFileName": {"undefined"}, "Locale": {"en-US"}, "sessionID": {sid}}
	if err := msJSON(ctx, api+"GetProductDownloadLinksBySku?"+q.Encode(), map[string]string{"Referer": msWWW + rp.page}, &links); err != nil {
		return "", err
	}
	if err := links.Errors.err(unreached); err != nil {
		return "", err
	}
	for _, o := range links.Options {
		if o.Type == 1 || strings.Contains(o.URI, "x64") { // 0 x86, 1 x64, 2 arm64
			return o.URI, nil
		}
	}
	return "", errors.New("Microsoft offered no 64-bit ISO")
}

func msJSON(ctx context.Context, u string, hdr map[string]string, v any) error {
	res, b, err := msGet(ctx, u, hdr, 1<<20)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Microsoft's download API answered %s", res.Status)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("Microsoft's download API: %v", err)
	}
	return nil
}

var winFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,200}\.iso$`)

// HasRetailISO reports whether Microsoft offers a retail ISO of edition.
func HasRetailISO(edition string) bool {
	_, ok := retailPages[edition]
	return ok
}

// RetailWindowsISO resolves a fresh download of the multi-edition ISO of
// edition (win11, win10) in a language such as hu-HU.
func RetailWindowsISO(ctx context.Context, edition, locale string) (*WinISO, error) {
	link, err := retailISO(ctx, edition, locale)
	if err != nil {
		return nil, err
	}
	pu, err := url.Parse(link)
	if err != nil || pu.Scheme != "https" || !msHostOK(pu.Hostname()) {
		return nil, errors.New("Microsoft offered a link outside microsoft.com")
	}
	file := path.Base(pu.Path)
	if !winFileRe.MatchString(file) {
		return nil, fmt.Errorf("unexpected file name %q", file)
	}
	return &WinISO{URL: link, File: file}, nil
}
