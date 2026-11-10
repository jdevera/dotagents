package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	return f(request)
}

// Exercise real client/request/redirect behavior without binding a socket.
func githubTestClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{
		CheckRedirect: githubRedirect,
		Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			response := recorder.Result()
			response.Request = request
			return response, nil
		}),
	}
}

func TestLatestCommitUsesRepositoryAPIAndBoundedTokenHeader(t *testing.T) {
	var path, authorization string
	client := githubTestClient(func(w http.ResponseWriter, r *http.Request) {
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"sha":%q}`, strings.ToUpper(testCommit))
	})
	commit, err := latestCommit(context.Background(), "https://github.com/Owner/Repo.git/skills/example", "https://api.github.com", client, "private-token")
	if err != nil || commit != testCommit || path != "/repos/Owner/Repo/commits/HEAD" || authorization != "Bearer private-token" {
		t.Fatalf("lookup mismatch: commit=%s path=%s error=%v", commit, path, err)
	}
}

func TestLatestCommitRejectsInvalidSourcesAndPayloads(t *testing.T) {
	for _, source := range []string{"../repo", "owner/repo/../other", "https://github.com@evil.test/owner/repo", "https://token@github.com/owner/repo", "owner/repo?token=secret", "--flag"} {
		if _, _, err := githubRepository(source); err == nil {
			t.Fatalf("accepted unsafe source: %q", source)
		}
	}
	for _, body := range []string{`{"sha":"main"}`, `{"sha":123}`, `private-error`, strings.Repeat("x", (1<<20)+1)} {
		client := githubTestClient(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		_, err := latestCommit(context.Background(), "owner/repo", "https://api.github.com", client, "private-token")
		if err == nil || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "private-error") {
			t.Fatalf("invalid response accepted or private values leaked: %v", err)
		}
	}
}

func TestLatestCommitHTTPFailureAndCancellation(t *testing.T) {
	client := githubTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("private server details"))
	})
	_, err := latestCommit(context.Background(), "owner/repo", "https://api.github.com", client, "")
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "private server details") {
		t.Fatalf("unhelpful or unsafe error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := latestCommit(ctx, "owner/repo", "https://api.github.com", client, ""); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestLatestCommitDoesNotFollowUntrustedRedirect(t *testing.T) {
	calledUntrusted := false
	client := githubTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.github.com" {
			calledUntrusted = true
			return
		}
		http.Redirect(w, r, "https://evil.test/token-collector", http.StatusFound)
	})
	_, err := latestCommit(context.Background(), "owner/repo", "https://api.github.com", client, "private-token")
	if err == nil || calledUntrusted {
		t.Fatal("lookup followed an untrusted redirect")
	}
}
