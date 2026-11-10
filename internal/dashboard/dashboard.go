// Package dashboard serves the local, read-only view of a dotagents report.
package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

//go:embed web/*
var assets embed.FS

// Export produces a self-contained report that works without a running server.
// JSON is escaped by encoding/json before it enters an inert data block; the
// executable script and stylesheet are authorized by content hashes.
func Export(data any) ([]byte, error) {
	report, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		return nil, err
	}
	css, err := assets.ReadFile("web/style.css")
	if err != nil {
		return nil, err
	}
	js, err := assets.ReadFile("web/app.js")
	if err != nil {
		return nil, err
	}
	hash := func(content []byte) string {
		digest := sha256.Sum256(content)
		return base64.StdEncoding.EncodeToString(digest[:])
	}
	csp := fmt.Sprintf(`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'sha256-%s'; style-src 'sha256-%s'; base-uri 'none'; form-action 'none'">`, hash(js), hash(css))
	html = bytes.Replace(html, []byte(`<link rel="stylesheet" href="/style.css">`), []byte("<style>"+string(css)+"</style>"), 1)
	html = bytes.Replace(html, []byte(`<script src="/app.js" defer></script>`), []byte(csp), 1)
	html = bytes.Replace(html, []byte(`href="/" aria-label="dotagents home"`), []byte(`href="#page-title" aria-label="dotagents home"`), 1)
	scripts := append([]byte(`<script id="embedded-report" type="application/json">`), report...)
	scripts = append(scripts, []byte("</script><script>")...)
	scripts = append(scripts, js...)
	scripts = append(scripts, []byte("</script>\n</body>")...)
	html = bytes.Replace(html, []byte("</body>"), scripts, 1)
	return html, nil
}

// Handler returns a read-only dashboard. The caller must bind its server to a
// loopback address and validate the Host header. Reports contain local paths;
// neither the API nor the embedded assets permit cross-origin access.
func Handler(data func() (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "This dashboard is read-only.", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-site requests are not allowed.", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "Cross-origin requests are not allowed.", http.StatusForbidden)
				return
			}
		}
		if r.URL.Path == "/api/report" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if r.Method == http.MethodHead {
				return
			}
			report, err := data()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"Could not read the local report. Run dotagents check in a terminal for details."}`))
				return
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"Could not encode the local report. Run dotagents check in a terminal for details."}`))
				return
			}
			_, _ = w.Write(encoded)
			return
		}
		var name, mime string
		switch r.URL.Path {
		case "/":
			name, mime = "index.html", "text/html; charset=utf-8"
		case "/app.js":
			name, mime = "app.js", "text/javascript; charset=utf-8"
		case "/style.css":
			name, mime = "style.css", "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		content, err := assets.ReadFile("web/" + name)
		if err != nil || strings.Contains(r.URL.RawPath, "..") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
	})
}
