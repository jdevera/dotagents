package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// LatestCommit resolves the current default-branch commit through GitHub's
// official API. It does not clone, execute repository contents, or update tools.
// GITHUB_TOKEN is sent only to the fixed trusted HTTPS API origin.
func LatestCommit(ctx context.Context, source string) (string, error) {
	client := &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: githubRedirect,
	}
	return latestCommit(ctx, source, "https://api.github.com", client, os.Getenv("GITHUB_TOKEN"))
}

func githubRedirect(request *http.Request, previous []*http.Request) error {
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.URL.User != nil || len(previous) >= 3 {
		return errors.New("GitHub API redirect refused")
	}
	return nil
}

func githubRepository(source string) (owner, repository string, err error) {
	if !githubSource.MatchString(source) {
		return "", "", errors.New("source must be a GitHub owner/repository or GitHub URL without credentials, query, or fragment")
	}
	for _, prefix := range []string{"https://github.com/", "github.com/", "git@github.com:"} {
		source = strings.TrimPrefix(source, prefix)
	}
	parts := strings.Split(source, "/")
	if len(parts) < 2 {
		return "", "", errors.New("GitHub source needs an owner and repository")
	}
	for _, part := range parts {
		if part == "." || part == ".." {
			return "", "", errors.New("GitHub source cannot contain dot path segments")
		}
	}
	repository = strings.TrimSuffix(parts[1], ".git")
	if repository == "" || repository == "." || repository == ".." {
		return "", "", errors.New("invalid GitHub repository name")
	}
	return parts[0], repository, nil
}

// The endpoint/client parameters are internal seams for httptest. Production
// callers can only use LatestCommit's fixed trusted origin and redirect policy.
func latestCommit(ctx context.Context, source, endpoint string, client *http.Client, token string) (string, error) {
	owner, repository, err := githubRepository(source)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", errors.New("GITHUB_TOKEN contains invalid header characters")
	}
	requestURL := endpoint + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/commits/HEAD"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", errors.New("cannot construct GitHub request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "dotagents")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Do not echo transport details or headers, which can contain tokens.
		return "", errors.New("GitHub commit lookup failed (network, timeout, or refused redirect)")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub commit lookup returned HTTP %d; check repository access and API rate limits", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return "", errors.New("GitHub commit response could not be read within the size limit")
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(data, &result); err != nil || !commitRef.MatchString(result.SHA) {
		return "", errors.New("GitHub did not return a valid full commit SHA")
	}
	return strings.ToLower(result.SHA), nil
}
