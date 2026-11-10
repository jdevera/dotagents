package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportIsReadOnlyAndNotCached(t *testing.T) {
	calls := 0
	handler := Handler(func() (any, error) {
		calls++
		return map[string]any{"name": "a <script> skill", "findings": []string{}}, nil
	})
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/report", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status %d, calls %d", recorder.Code, calls)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("local report must not be cached or shared with other origins")
	}
	var report map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["name"] != "a <script> skill" {
		t.Fatalf("report changed: %v", report)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, "http://localhost/api/report", nil))
		if recorder.Code != http.StatusMethodNotAllowed || calls != 1 {
			t.Fatalf("%s status %d calls %d", method, recorder.Code, calls)
		}
	}
}

func TestCrossOriginCannotReadLocalInventory(t *testing.T) {
	calls := 0
	handler := Handler(func() (any, error) { calls++; return nil, nil })
	for _, headers := range []map[string]string{
		{"Origin": "https://example.com"},
		{"Origin": "null"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "http://localhost:9999"},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://localhost/api/report", nil)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || calls != 0 {
			t.Fatalf("headers %v: status %d, calls %d", headers, w.Code, calls)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/report", nil)
	r.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("same origin request failed: %d", w.Code)
	}
}

func TestErrorsDoNotExposePrivatePathsOrCredentials(t *testing.T) {
	for _, data := range []func() (any, error){
		func() (any, error) { return nil, errors.New("cannot read /private/token-secret") },
		func() (any, error) { return make(chan int), nil },
	} {
		w := httptest.NewRecorder()
		Handler(data).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/api/report", nil))
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "token-secret") {
			t.Fatalf("unsafe error: status %d, body %s", w.Code, w.Body.String())
		}
		var response map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response["error"] == "" {
			t.Fatalf("error response must be readable JSON: %s", w.Body.String())
		}
	}
}

func TestOnlyEmbeddedAssetsAreServed(t *testing.T) {
	handler := Handler(func() (any, error) { t.Fatal("assets invoked report scan"); return nil, nil })
	for path, contentType := range map[string]string{"/": "text/html", "/app.js": "text/javascript", "/style.css": "text/css"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), contentType) || w.Body.Len() == 0 {
			t.Fatalf("asset %s failed: %d", path, w.Code)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("missing frame protection")
		}
	}
	for _, path := range []string{"/../README.md", "/web/index.html", "/%2e%2e/README.md", "/app.js/extra", "/api/mutate", "/index.html"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unexpected asset served %s: %d", path, w.Code)
		}
	}
}

func TestHeadDoesNotScan(t *testing.T) {
	handler := Handler(func() (any, error) { t.Fatal("HEAD must not scan"); return nil, nil })
	for _, path := range []string{"/", "/api/report", "/app.js"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "http://localhost"+path, nil))
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Fatalf("HEAD %s status %d bytes %d", path, w.Code, w.Body.Len())
		}
	}
}

func TestExportIsSelfContainedAndEscapesReport(t *testing.T) {
	html, err := Export(map[string]string{"name": `</script><script>alert("private")</script>`})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, unexpected := range []string{`src="/app.js"`, `href="/style.css"`, `</script><script>alert("private")`} {
		if strings.Contains(page, unexpected) {
			t.Fatalf("export contains %q", unexpected)
		}
	}
	if !strings.Contains(page, `id="embedded-report" type="application/json"`) || !strings.Contains(page, `\u003c/script\u003e`) || !strings.Contains(page, "script-src 'sha256-") {
		t.Fatal("export is missing escaped data or script authorization")
	}
	if _, err := Export(make(chan int)); err == nil {
		t.Fatal("export should reject unencodable report")
	}
}
