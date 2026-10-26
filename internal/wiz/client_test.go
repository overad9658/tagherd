package wiz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testQuery = json.RawMessage(`{"type":["VIRTUAL_MACHINE_IMAGE"],"select":true,"where":{"nativeType":{"EQUALS":["ami"]}}}`)

// stubWiz serves the token endpoint and graphSearch. page answers each
// GraphQL call, numbered from 1, given its variables.
type stubWiz struct {
	tokens, calls atomic.Int32
	page          func(t *testing.T, n int, vars map[string]json.RawMessage) (int, string)
}

func (s *stubWiz) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("audience") != "wiz-api" ||
				r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret" {
				http.Error(w, `{"error":"access_denied"}`, http.StatusUnauthorized)
				return
			}
			n := s.tokens.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600,"token_type":"Bearer"}`, n)
		case "/graphql":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body struct {
				Query     string                     `json:"query"`
				Variables map[string]json.RawMessage `json:"variables"`
			}
			data, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(data, &body); err != nil || !strings.Contains(body.Query, "graphSearch") {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			status, resp := s.page(t, int(s.calls.Add(1)), body.Variables)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, resp)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestClient(t *testing.T, srv *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(Options{
		APIURL: srv.URL + "/graphql", AuthURL: srv.URL + "/oauth/token", ClientID: "id", ClientSecret: "secret",
		Timeout: timeout, Backoff: func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func pageJSON(hasNext bool, cursor string, ids ...string) string {
	var nodes []string
	for _, id := range ids {
		nodes = append(nodes, fmt.Sprintf(`{"entities":[{"id":"w-%s","type":"VIRTUAL_MACHINE_IMAGE","properties":{"externalId":%q,"providerUniqueId":"arn:aws:ec2:us-east-1:1:image/%s"}}]}`, id, id, id))
	}
	return fmt.Sprintf(`{"data":{"graphSearch":{"maxCountReached":false,"pageInfo":{"endCursor":%q,"hasNextPage":%v},"nodes":[%s]}}}`,
		cursor, hasNext, strings.Join(nodes, ","))
}

func TestSearchPagesAndCaches(t *testing.T) {
	s := &stubWiz{page: func(t *testing.T, n int, vars map[string]json.RawMessage) (int, string) {
		// The override is sent as the query variable, unchanged.
		if string(vars["query"]) != string(testQuery) || string(vars["projectId"]) != `"*"` {
			t.Errorf("variables = %s", vars)
		}
		if n == 1 {
			if _, ok := vars["after"]; ok {
				t.Error("the first page has no cursor")
			}
			return 200, pageJSON(true, "c1", "ami-1", "ami-2")
		}
		if string(vars["after"]) != `"c1"` {
			t.Errorf("after = %s", vars["after"])
		}
		return 200, pageJSON(false, "", "ami-3")
	}}
	srv := s.serve(t)
	defer srv.Close()
	c := newTestClient(t, srv, 0)

	got, err := c.Search(context.Background(), &Query{Raw: testQuery})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, id := range []string{"ami-1", "ami-2", "ami-3"} {
		want[id], want["arn:aws:ec2:us-east-1:1:image/"+id] = true, true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if s.tokens.Load() != 1 {
		t.Errorf("tokens = %d, want one for both pages", s.tokens.Load())
	}

	// The same query again, as for another group or account, is not re-sent.
	if _, err := c.Run(context.Background(), &Query{Raw: testQuery}, nil); err != nil || s.calls.Load() != 2 {
		t.Errorf("calls = %d, err %v", s.calls.Load(), err)
	}
}

func TestSearchFailures(t *testing.T) {
	tests := []struct {
		name string
		page func(t *testing.T, n int, vars map[string]json.RawMessage) (int, string)
		want string
	}{
		{"capped results are never used", func(*testing.T, int, map[string]json.RawMessage) (int, string) {
			return 200, `{"data":{"graphSearch":{"maxCountReached":true,"pageInfo":{"hasNextPage":false},"nodes":[]}}}`
		}, "maxCountReached"},
		{"GraphQL errors", func(*testing.T, int, map[string]json.RawMessage) (int, string) {
			return 200, `{"errors":[{"message":"invalid field nativeTyp"}]}`
		}, "invalid field nativeTyp"},
		{"client error is not retried", func(*testing.T, int, map[string]json.RawMessage) (int, string) {
			return 403, `{"message":"forbidden"}`
		}, "403"},
		{"server errors exhaust retries", func(*testing.T, int, map[string]json.RawMessage) (int, string) {
			return 503, "down"
		}, "503"},
		{"a next page without a cursor", func(*testing.T, int, map[string]json.RawMessage) (int, string) {
			return 200, pageJSON(true, "", "ami-1")
		}, "endCursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubWiz{page: tc.page}
			srv := s.serve(t)
			defer srv.Close()
			_, err := newTestClient(t, srv, 0).Search(context.Background(), &Query{Raw: testQuery})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSearchRetries(t *testing.T) {
	s := &stubWiz{page: func(_ *testing.T, n int, _ map[string]json.RawMessage) (int, string) {
		switch n {
		case 1:
			return http.StatusTooManyRequests, "slow down"
		case 2:
			return http.StatusUnauthorized, "token expired"
		}
		return 200, pageJSON(false, "", "ami-9")
	}}
	srv := s.serve(t)
	defer srv.Close()
	got, err := newTestClient(t, srv, 0).Search(context.Background(), &Query{Raw: testQuery})
	if err != nil || !got["ami-9"] {
		t.Fatalf("got %v, err %v", got, err)
	}
	if s.tokens.Load() != 2 {
		t.Errorf("a 401 gets a new token: tokens = %d", s.tokens.Load())
	}
}

func TestSearchTimeout(t *testing.T) {
	s := &stubWiz{page: func(*testing.T, int, map[string]json.RawMessage) (int, string) {
		time.Sleep(200 * time.Millisecond)
		return 200, pageJSON(false, "")
	}}
	srv := s.serve(t)
	defer srv.Close()
	_, err := newTestClient(t, srv, 50*time.Millisecond).Search(context.Background(), &Query{Raw: testQuery})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestAuthFailure(t *testing.T) {
	s := &stubWiz{page: func(*testing.T, int, map[string]json.RawMessage) (int, string) { return 200, pageJSON(false, "") }}
	srv := s.serve(t)
	defer srv.Close()
	c, _ := NewClient(Options{APIURL: srv.URL + "/graphql", AuthURL: srv.URL + "/oauth/token", ClientID: "id", ClientSecret: "wrong"})
	if _, err := c.Search(context.Background(), &Query{Raw: testQuery}); err == nil || !strings.Contains(err.Error(), "wiz auth: 401") {
		t.Errorf("err = %v", err)
	}
}

func TestNewClient(t *testing.T) {
	if _, err := NewClient(Options{ClientID: "a", ClientSecret: "b"}); err == nil {
		t.Error("api_url is required")
	}
	if _, err := NewClient(Options{APIURL: "https://x", ClientID: "a"}); err == nil {
		t.Error("client_secret is required")
	}
	c, err := NewClient(Options{APIURL: "https://x", ClientID: "a", ClientSecret: "b"})
	if err != nil || c.opts.AuthURL != DefaultAuthURL || c.opts.Timeout != DefaultTimeout {
		t.Errorf("defaults: %+v %v", c.opts, err)
	}
}
