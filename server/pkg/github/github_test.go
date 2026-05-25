package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		in        string
		owner     string
		repo      string
		wantError bool
	}{
		{"https://github.com/multica-ai/multica", "multica-ai", "multica", false},
		{"https://github.com/multica-ai/multica.git", "multica-ai", "multica", false},
		{"https://github.com/multica-ai/multica/", "multica-ai", "multica", false},
		{"  https://github.com/owner/repo  ", "owner", "repo", false},
		{"http://github.com/owner/repo", "", "", true},
		{"https://gitlab.com/owner/repo", "", "", true},
		{"https://github.com/", "", "", true},
		{"https://github.com/owner", "", "", true},
		{"not a url", "", "", true},
	}
	for _, tt := range tests {
		owner, repo, err := ParseRepoURL(tt.in)
		if tt.wantError {
			if err == nil {
				t.Errorf("ParseRepoURL(%q): expected error, got %s/%s", tt.in, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepoURL(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if owner != tt.owner || repo != tt.repo {
			t.Errorf("ParseRepoURL(%q): got %s/%s, want %s/%s", tt.in, owner, repo, tt.owner, tt.repo)
		}
	}
}

// TestListPullRequests_HappyPath verifies the Authorization header, the
// query string GitHub wants, and the JSON decode path all work end-to-end
// against a mocked GitHub server.
func TestListPullRequests_HappyPath(t *testing.T) {
	body := `[{
        "number": 42, "title": "Add Ship Hub", "state": "open", "draft": false,
        "html_url": "https://github.com/owner/repo/pull/42",
        "body": "summary",
        "user": {"login": "alice", "avatar_url": "https://example.com/a.png"},
        "base": {"ref": "main"},
        "head": {"ref": "feat/ship-hub", "sha": "abc123"},
        "labels": [{"name": "feat", "color": "00ff00"}],
        "additions": 100, "deletions": 50, "changed_files": 5,
        "created_at": "2026-04-30T00:00:00Z", "updated_at": "2026-05-01T00:00:00Z"
    }]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header: got %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("accept header: got %q", got)
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state query: got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("test-token")
	c.BaseURL = srv.URL
	prs, err := c.ListPullRequests(context.Background(), "owner", "repo", ListOptions{})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("len(prs): got %d", len(prs))
	}
	pr := prs[0]
	if pr.Number != 42 || pr.Title != "Add Ship Hub" || pr.State != "open" {
		t.Errorf("unexpected PR: %+v", pr)
	}
	if pr.User.Login != "alice" || pr.Head.SHA != "abc123" || len(pr.Labels) != 1 {
		t.Errorf("unexpected nested fields: %+v", pr)
	}
	if pr.UpdatedAt.IsZero() || !pr.UpdatedAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt: got %v", pr.UpdatedAt)
	}
}

// TestListPullRequests_ErrorMapping covers the four GitHub failure modes
// we care about. Each must map to a distinct typed error so the Ship Hub
// service can decide whether to retry, surface to the user, or back off.
func TestListPullRequests_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		wantErr error
	}{
		{"not found", http.StatusNotFound, nil, "", ErrNotFound},
		{"unauthorized", http.StatusUnauthorized, nil, "", ErrUnauthorized},
		{"primary rate limit", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, "", ErrRateLimited},
		{"secondary rate limit", http.StatusForbidden, nil, `{"message":"You have exceeded a secondary rate limit"}`, ErrRateLimited},
		{"forbidden non-rate", http.StatusForbidden, nil, `{"message":"Resource not accessible by integration"}`, ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				if tc.body != "" {
					w.Write([]byte(tc.body))
				}
			}))
			defer srv.Close()

			c := NewClient("t")
			c.BaseURL = srv.URL
			_, err := c.ListPullRequests(context.Background(), "o", "r", ListOptions{})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestGetCIStatus_CombinesLegacyAndCheckRuns — ROA-274. The legacy
// /status endpoint is blind to GitHub Actions (check-runs), so a
// red Actions run must still surface as "failure" via the check-runs
// rollup. Also covers the CI-less ("" + "" → "") and pending cases.
func TestGetCIStatus_CombinesLegacyAndCheckRuns(t *testing.T) {
	cases := []struct {
		name string
		// legacyBody is the raw JSON the combined-status endpoint
		// returns. GitHub reports `state:"pending", total_count:0` for a
		// commit with NO legacy statuses — every commit in an
		// Actions-only repo — so the realistic Actions-only body is NOT
		// `{"state":""}`.
		legacyBody string
		checkRuns  string // JSON body for the check-runs endpoint
		want       string
	}{
		{
			name:       "actions failure, Actions-only repo (legacy pending/0) → failure",
			legacyBody: `{"state":"pending","total_count":0}`,
			checkRuns:  `{"total_count":2,"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"}]}`,
			want:       "failure",
		},
		{
			// Regression for the prellr/multica bug: an Actions-only repo
			// with all check-runs green. The legacy endpoint says
			// "pending" purely because total_count==0 — that must NOT
			// mask the green rollup.
			name:       "actions all green, Actions-only repo (legacy pending/0) → success",
			legacyBody: `{"state":"pending","total_count":0}`,
			checkRuns:  `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`,
			want:       "success",
		},
		{
			name:       "actions still running → pending",
			legacyBody: `{"state":"pending","total_count":0}`,
			checkRuns:  `{"total_count":1,"check_runs":[{"status":"in_progress","conclusion":""}]}`,
			want:       "pending",
		},
		{
			name:       "CI-less repo (no legacy, no checks) → empty",
			legacyBody: `{"state":"pending","total_count":0}`,
			checkRuns:  `{"total_count":0,"check_runs":[]}`,
			want:       "",
		},
		{
			name:       "legacy failure dominates green checks → failure",
			legacyBody: `{"state":"failure","total_count":1}`,
			checkRuns:  `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`,
			want:       "failure",
		},
		{
			// A genuine in-flight legacy status (total_count>0) must
			// still dominate a green check-run rollup as pending.
			name:       "real in-flight legacy status (pending/1) → pending",
			legacyBody: `{"state":"pending","total_count":1}`,
			checkRuns:  `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`,
			want:       "pending",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
					w.Write([]byte(tc.legacyBody))
				case strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
					w.Write([]byte(tc.checkRuns))
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			c := NewClient("")
			c.BaseURL = srv.URL
			got, err := c.GetCIStatus(context.Background(), "o", "r", "abc")
			if err != nil {
				t.Fatalf("GetCIStatus: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetCIStatus: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetCombinedStatus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// A repo with real legacy statuses — state is reported verbatim.
			name: "has statuses → state passes through",
			body: `{"state":"success","total_count":2}`,
			want: "success",
		},
		{
			// Actions-only repo: GitHub reports state "pending" purely
			// because there are zero legacy statuses. total_count==0 maps
			// that phantom pending to "" so it can't mask a check-run
			// rollup downstream in GetCIStatus.
			name: "zero statuses → empty, not phantom pending",
			body: `{"state":"pending","total_count":0}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/commits/abc/status") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient("")
			c.BaseURL = srv.URL
			state, err := c.GetCombinedStatus(context.Background(), "o", "r", "abc")
			if err != nil {
				t.Fatalf("GetCombinedStatus: %v", err)
			}
			if state != tc.want {
				t.Errorf("state: got %q, want %q", state, tc.want)
			}
		})
	}
}

// TestListWorkflowRuns_HappyPath verifies the deploy poller's read
// path — branch/status/per_page query params, the workflow file name
// in the URL path, the wrapper-object decode (GitHub returns
// `{ "workflow_runs": [...] }` not a bare array).
func TestListWorkflowRuns_HappyPath(t *testing.T) {
	body := `{
        "total_count": 1,
        "workflow_runs": [{
            "id": 9876,
            "name": "Deploy production",
            "head_sha": "deadbeef",
            "head_branch": "main",
            "status": "completed",
            "conclusion": "success",
            "html_url": "https://github.com/o/r/actions/runs/9876",
            "created_at": "2026-05-09T10:00:00Z",
            "updated_at": "2026-05-09T10:05:00Z",
            "run_started_at": "2026-05-09T10:00:01Z"
        }]
    }`
	var seenPath, seenBranch, seenStatus, seenPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenBranch = r.URL.Query().Get("branch")
		seenStatus = r.URL.Query().Get("status")
		seenPerPage = r.URL.Query().Get("per_page")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient("test-token")
	c.BaseURL = srv.URL
	runs, err := c.ListWorkflowRuns(context.Background(), "o", "r", "production.yml", ListWorkflowRunsOptions{
		Branch:  "main",
		Status:  "completed",
		PerPage: 10,
	})
	if err != nil {
		t.Fatalf("ListWorkflowRuns: %v", err)
	}
	if seenPath != "/repos/o/r/actions/workflows/production.yml/runs" {
		t.Errorf("path: got %q", seenPath)
	}
	if seenBranch != "main" || seenStatus != "completed" || seenPerPage != "10" {
		t.Errorf("query: branch=%q status=%q per_page=%q", seenBranch, seenStatus, seenPerPage)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs): got %d", len(runs))
	}
	r := runs[0]
	if r.ID != 9876 || r.HeadSHA != "deadbeef" || r.Conclusion != "success" || r.HeadBranch != "main" {
		t.Errorf("unexpected run: %+v", r)
	}
}

// TestListWorkflowRuns_EmptyWorkflowName guards the error path that
// keeps the poller from accidentally hitting `/runs?...` (no workflow
// id) which would 404 against every GitHub repo and burn rate budget.
func TestListWorkflowRuns_EmptyWorkflowName(t *testing.T) {
	c := NewClient("t")
	if _, err := c.ListWorkflowRuns(context.Background(), "o", "r", "", ListWorkflowRunsOptions{}); err == nil {
		t.Fatal("expected error for empty workflow name")
	}
}

// TestListWorkflowRuns_DefaultPerPage ensures the per_page default is
// applied when the caller passes 0 (and clamped to 100 when > 100).
func TestListWorkflowRuns_DefaultPerPage(t *testing.T) {
	var seenPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPerPage = r.URL.Query().Get("per_page")
		w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	c := NewClient("t")
	c.BaseURL = srv.URL
	if _, err := c.ListWorkflowRuns(context.Background(), "o", "r", "p.yml", ListWorkflowRunsOptions{}); err != nil {
		t.Fatalf("ListWorkflowRuns: %v", err)
	}
	if seenPerPage != "10" {
		t.Errorf("default per_page: got %q, want 10", seenPerPage)
	}

	if _, err := c.ListWorkflowRuns(context.Background(), "o", "r", "p.yml", ListWorkflowRunsOptions{PerPage: 500}); err != nil {
		t.Fatalf("ListWorkflowRuns clamp: %v", err)
	}
	if seenPerPage != "100" {
		t.Errorf("clamp per_page: got %q, want 100", seenPerPage)
	}
}

// TestUnauthClientNoAuthHeader verifies we don't accidentally send a bare
// "Bearer " (which some servers reject) when no token is set.
func TestUnauthClientNoAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no auth header, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.BaseURL = srv.URL
	if _, err := c.ListPullRequests(context.Background(), "o", "r", ListOptions{}); err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
}

// TestDoWithBody_RateLimitRetry covers ROA-373's defensive backoff:
// a 403 with X-RateLimit-Remaining: 0 and a near-future X-RateLimit-Reset
// triggers exactly one short sleep + retry. A reset too far in the future
// (or no headers) falls straight through as ErrRateLimited so the caller
// sees the real exhaustion.
func TestDoWithBody_RateLimitRetry(t *testing.T) {
	t.Run("retries once on primary rate-limit with usable Retry-After", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		c := NewClient("")
		c.BaseURL = srv.URL
		start := time.Now()
		err := c.do(context.Background(), "GET", "/anything", &struct{}{})
		if err != nil {
			t.Fatalf("expected retry to succeed, got %v", err)
		}
		if calls != 2 {
			t.Errorf("expected 2 attempts, got %d", calls)
		}
		if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
			t.Errorf("expected at least ~1s wait between attempts, got %v", elapsed)
		}
	})

	t.Run("does not retry when reset is far in the future", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			// Reset 10 minutes out — beyond maxRetryWait (60s). Should
			// surface as ErrRateLimited without sleeping or retrying.
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset",
				strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := NewClient("")
		c.BaseURL = srv.URL
		start := time.Now()
		err := c.do(context.Background(), "GET", "/anything", &struct{}{})
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected ErrRateLimited, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected exactly 1 attempt (no retry), got %d", calls)
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("expected immediate return without sleep, took %v", elapsed)
		}
	})

	t.Run("respects context cancellation during backoff", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "5") // would sleep 5s if not cancelled
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := NewClient("")
		c.BaseURL = srv.URL
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		err := c.do(ctx, "GET", "/anything", &struct{}{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 attempt before cancel, got %d", calls)
		}
	})

	t.Run("does not retry on plain 403 (forbidden, not rate-limit)", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			// No rate-limit headers — a real permission 403.
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := NewClient("")
		c.BaseURL = srv.URL
		err := c.do(context.Background(), "GET", "/anything", &struct{}{})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected ErrForbidden, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 attempt (no retry on plain 403), got %d", calls)
		}
	})
}

// TestDoWithBody_PATFallbackOn404 covers the ROA-379 follow-up: when a
// GitHub-App installation token returns 404 (repo not in installation
// scope), the client retries once with the configured PAT before
// surfacing ErrNotFound. Cross-namespace projects that the App doesn't
// cover but the operator's PAT does keep working.
func TestDoWithBody_PATFallbackOn404(t *testing.T) {
	t.Run("404 on App → retry with PAT → success", func(t *testing.T) {
		var sawAuth []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			sawAuth = append(sawAuth, auth)
			if auth == "Bearer app-token-xyz" {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(
			staticToken("app-token-xyz"),
			"pat-token-abc",
		)
		c.BaseURL = srv.URL
		if err := c.do(context.Background(), "GET", "/repos/other-org/repo/pulls", &[]struct{}{}); err != nil {
			t.Fatalf("expected fallback success, got %v", err)
		}
		if len(sawAuth) != 2 {
			t.Fatalf("expected 2 attempts (App then PAT), got %d: %v", len(sawAuth), sawAuth)
		}
		if sawAuth[0] != "Bearer app-token-xyz" {
			t.Errorf("first attempt should use App token, got %q", sawAuth[0])
		}
		if sawAuth[1] != "Bearer pat-token-abc" {
			t.Errorf("second attempt should use PAT, got %q", sawAuth[1])
		}
	})
	t.Run("404 on App → 404 on PAT → ErrNotFound surfaces", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL
		err := c.do(context.Background(), "GET", "/repos/gone/repo/pulls", &[]struct{}{})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
		if calls != 2 {
			t.Errorf("expected 2 attempts (App + PAT both 404), got %d", calls)
		}
	})
	t.Run("happy path: no fallback when 2xx on App", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL
		if err := c.do(context.Background(), "GET", "/repos/covered/repo/pulls", &[]struct{}{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 attempt on 2xx, got %d", calls)
		}
	})
	t.Run("no fallback when FallbackToken is empty", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}))
		defer srv.Close()
		c := NewClientWithTokenSource(staticToken("app-only"))
		c.BaseURL = srv.URL
		err := c.do(context.Background(), "GET", "/anything", &struct{}{})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 attempt (no fallback configured), got %d", calls)
		}
	})
	t.Run("subsequent calls to same owner skip App when negative cache is warm", func(t *testing.T) {
		var appCalls, patCalls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer app" {
				atomic.AddInt32(&appCalls, 1)
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			atomic.AddInt32(&patCalls, 1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL

		// Pretend the cache uses a fixed "now" so the test doesn't
		// race the wall clock if it ever runs near the TTL boundary.
		c.appMissNow = func() time.Time { return time.Unix(1_700_000_000, 0) }

		// First call: App 404 → PAT success. Records the miss.
		if err := c.do(context.Background(), "GET", "/repos/imjenaro/saf-mobile-ios/pulls", &[]struct{}{}); err != nil {
			t.Fatalf("first call: %v", err)
		}
		if got := atomic.LoadInt32(&appCalls); got != 1 {
			t.Errorf("after first call: appCalls=%d, want 1", got)
		}
		if got := atomic.LoadInt32(&patCalls); got != 1 {
			t.Errorf("after first call: patCalls=%d, want 1", got)
		}

		// Second + third calls to same owner: cache hit → skip App.
		for i, path := range []string{"/repos/imjenaro/saf-mobile-ios/commits/abc/status", "/repos/imjenaro/control-panel/pulls"} {
			if err := c.do(context.Background(), "GET", path, &[]struct{}{}); err != nil {
				t.Fatalf("subsequent call %d (%s): %v", i+2, path, err)
			}
		}
		if got := atomic.LoadInt32(&appCalls); got != 1 {
			t.Errorf("after cache-hit calls: appCalls=%d, want 1 (no further App attempts)", got)
		}
		if got := atomic.LoadInt32(&patCalls); got != 3 {
			t.Errorf("after cache-hit calls: patCalls=%d, want 3 (one per call)", got)
		}

		// Other owner is not poisoned by the imjenaro miss.
		if err := c.do(context.Background(), "GET", "/repos/prellr/multica/pulls", &[]struct{}{}); err != nil {
			t.Fatalf("prellr call: %v", err)
		}
		if got := atomic.LoadInt32(&appCalls); got != 2 {
			t.Errorf("after prellr call: appCalls=%d, want 2 (prellr still tries App)", got)
		}
	})
	t.Run("cache expires after TTL", func(t *testing.T) {
		var appCalls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer app" {
				atomic.AddInt32(&appCalls, 1)
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL
		c.AppMissOwnerTTL = 10 * time.Minute
		now := time.Unix(1_700_000_000, 0)
		c.appMissNow = func() time.Time { return now }

		// Warm the cache.
		_ = c.do(context.Background(), "GET", "/repos/foo/bar/pulls", &[]struct{}{})
		if got := atomic.LoadInt32(&appCalls); got != 1 {
			t.Errorf("warmup: appCalls=%d, want 1", got)
		}
		// Within TTL — cache hit; appCalls stays at 1.
		_ = c.do(context.Background(), "GET", "/repos/foo/bar/pulls", &[]struct{}{})
		if got := atomic.LoadInt32(&appCalls); got != 1 {
			t.Errorf("within TTL: appCalls=%d, want 1", got)
		}
		// Advance past TTL — cache miss; App is tried again.
		now = now.Add(11 * time.Minute)
		_ = c.do(context.Background(), "GET", "/repos/foo/bar/pulls", &[]struct{}{})
		if got := atomic.LoadInt32(&appCalls); got != 2 {
			t.Errorf("after TTL expiry: appCalls=%d, want 2", got)
		}
	})
	t.Run("non-repos path does not populate cache", func(t *testing.T) {
		var appCalls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer app" {
				atomic.AddInt32(&appCalls, 1)
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL
		// 404 on /app/installations/123/foo — no owner to key on, so
		// cache stays empty. Subsequent repos call still tries App.
		_ = c.do(context.Background(), "GET", "/app/installations/1/anything", &struct{}{})
		_ = c.do(context.Background(), "GET", "/repos/some/repo/pulls", &[]struct{}{})
		if got := atomic.LoadInt32(&appCalls); got != 2 {
			t.Errorf("appCalls=%d, want 2 (no cache key from /app path)", got)
		}
	})
	t.Run("404 on App → rate-limited PAT → rate-limit retry on PAT → success", func(t *testing.T) {
		var attempts int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&attempts, 1)
			auth := r.Header.Get("Authorization")
			switch n {
			case 1:
				if auth != "Bearer app" {
					t.Errorf("attempt 1: expected App, got %q", auth)
				}
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			case 2:
				if auth != "Bearer pat" {
					t.Errorf("attempt 2: expected PAT, got %q", auth)
				}
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"message":"rate limit"}`, http.StatusForbidden)
			case 3:
				if auth != "Bearer pat" {
					t.Errorf("attempt 3: expected PAT (post-rate-limit), got %q", auth)
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("[]"))
			default:
				t.Errorf("unexpected 4th attempt")
			}
		}))
		defer srv.Close()
		c := NewClientWithTokenSourceAndFallback(staticToken("app"), "pat")
		c.BaseURL = srv.URL
		if err := c.do(context.Background(), "GET", "/anything", &[]struct{}{}); err != nil {
			t.Fatalf("expected eventual success, got %v", err)
		}
		if got := atomic.LoadInt32(&attempts); got != 3 {
			t.Errorf("expected 3 attempts (App-404 → PAT-403 → PAT-200), got %d", got)
		}
	})
}
