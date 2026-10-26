package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/wiz"
)

func TestWizClient(t *testing.T) {
	env := map[string]string{"WIZ_CLIENT_ID": "id", "WIZ_CLIENT_SECRET": "secret"}
	tests := []struct {
		name, yaml string
		want       string // "" means a client; else the fallback warning
	}{
		{"configured", "wiz:\n  api_url: https://api.us1.app.wiz.io/graphql\n  client_id: ${WIZ_CLIENT_ID}\n  client_secret: ${WIZ_CLIENT_SECRET}\n  timeout: 90s\n", ""},
		{"no block", "scopes: {}\n", "-"},
		{"unset secret", "wiz:\n  api_url: https://x\n  client_id: ${WIZ_CLIENT_ID}\n  client_secret: ${WIZ_NOPE}\n", "WIZ_NOPE"},
		{"no api_url", "wiz:\n  client_id: a\n  client_secret: b\n", "api_url"},
		{"bad timeout", "wiz:\n  api_url: https://x\n  client_id: a\n  client_secret: b\n  timeout: soon\n", "wiz.timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tc.yaml), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			a := &app{cfg: cfg, log: slog.New(slog.NewTextHandler(&logs, nil))}
			got := a.wizClient()
			switch tc.want {
			case "":
				if _, ok := got.(*wiz.Client); !ok {
					t.Errorf("no client: %s", logs.String())
				}
			case "-":
				if got != nil || logs.Len() != 0 {
					t.Errorf("without a wiz block there is no client and no warning: %v %s", got, logs.String())
				}
			default:
				if got != nil || !strings.Contains(logs.String(), tc.want) || !strings.Contains(logs.String(), "fall back") {
					t.Errorf("got %v, logs %s", got, logs.String())
				}
			}
		})
	}
}
