package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMicrosoft stands in for www, vlscppe, ov-df, go and the CDN.
func fakeMicrosoft(t *testing.T, sentinel bool) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/en-us/software-download/windows11":
			w.Write([]byte(`<select><option value="3813">Windows 11 (multi-edition ISO for x64 devices)</option></select>`))
		case r.URL.Path == "/tags":
		case r.URL.Path == "/mdt.js":
			w.Write([]byte(`var u="https://x/?a=1&w=ABC123";var rticks="+638900";`))
		case r.URL.Path == "/" && r.URL.Query().Get("w") == "ABC123":
		case strings.HasSuffix(r.URL.Path, "/getskuinformationbyproductedition"):
			if r.URL.Query().Get("productEditionId") != "3813" {
				t.Errorf("edition %q", r.URL.Query().Get("productEditionId"))
			}
			w.Write([]byte(`{"Skus":[{"Id":"1","Language":"English"},{"Id":"2","Language":"Hungarian"}]}`))
		case strings.HasSuffix(r.URL.Path, "/GetProductDownloadLinksBySku"):
			if sentinel {
				w.Write([]byte(`{"Errors":[{"Key":"ErrorSettings.SentinelReject","Value":"Sentinel marked this request as rejected.","Type":8}]}`))
				return
			}
			if r.URL.Query().Get("SKU") != "2" || r.Header.Get("Referer") == "" {
				t.Errorf("sku %q referer %q", r.URL.Query().Get("SKU"), r.Header.Get("Referer"))
			}
			w.Write([]byte(`{"ProductDownloadOptions":[{"Uri":"` + srv.URL + `/dbazure/Win11_25H2_Hungarian_Arm64.iso?t=1","DownloadType":2},{"Uri":"` + srv.URL + `/dbazure/Win11_25H2_Hungarian_x64.iso?t=1&e=2","DownloadType":1}]}`))
		default:
			t.Errorf("unexpected %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	saved := []string{msWWW, msTags, msOVDF}
	savedHTTP, savedOK := msHTTP, msHostOK
	t.Cleanup(func() { msWWW, msTags, msOVDF, msHTTP, msHostOK = saved[0], saved[1], saved[2], savedHTTP, savedOK })
	msWWW, msTags, msOVDF = srv.URL, srv.URL+"/tags", srv.URL+"/"
	c := srv.Client()
	c.CheckRedirect = msHTTP.CheckRedirect
	msHTTP = c
	msHostOK = func(h string) bool { return h == "127.0.0.1" }
}

func TestRetailWindowsISO(t *testing.T) {
	fakeMicrosoft(t, false)
	ctx := context.Background()
	iso, err := RetailWindowsISO(ctx, "win11", "hu-HU")
	if err != nil || iso.File != "Win11_25H2_Hungarian_x64.iso" || !strings.Contains(iso.URL, "e=2") {
		t.Fatalf("retail: %+v %v", iso, err)
	}
	if _, err := RetailWindowsISO(ctx, "ws2022", "en-US"); err == nil {
		t.Error("servers have no retail ISO")
	}
	if _, err := RetailWindowsISO(ctx, "win11", "xx-XX"); err == nil {
		t.Error("unknown language")
	}
}

func TestWindowsISORefused(t *testing.T) {
	fakeMicrosoft(t, true)
	_, err := RetailWindowsISO(context.Background(), "win11", "en-US")
	if err == nil || !strings.Contains(err.Error(), "715-123130") {
		t.Fatalf("got %v", err)
	}
}

func TestWindowsISOStaysOnMicrosoft(t *testing.T) {
	for _, u := range []string{"http://www.microsoft.com/x", "https://microsoft.com.evil.example/x", "https://evilmicrosoft.com/x"} {
		if _, _, err := msGet(context.Background(), u, nil, 1); err == nil {
			t.Errorf("%s was allowed", u)
		}
	}
}
