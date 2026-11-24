package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestUpgradeAndIndependentRoutes(t *testing.T) {
	dir := t.TempDir()
	pat, err := protect([]byte("private-fixture"), true)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]any{"version": 1, "retention_days": 21, "enabled": true, "base_url": "https://jira.example", "dpapi_pat": pat, "allowed": map[string]bool{"jira_search_text": true}})
	path := filepath.Join(dir, "settings.json")
	if err = os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := a.snapshot()
	j := s.Products["jira"]
	if !enabled(s, "jira", "jira_search_text") || enabled(s, "jira", "jira_search_jql") || s.Products["confluence"].Enabled || j.Mode != "direct" || s.Retention != 21 {
		t.Fatal("неверный перенос настроек")
	}
	if before, _ := os.ReadFile(path); string(before) != string(legacy) {
		t.Fatal("файл v1 изменён при чтении")
	}
	req := saveForm(a, "https://confluence.example/wiki", "wiki-token", true)
	req.ParseForm()
	req.PostForm.Set("service", "confluence")
	req.PostForm.Set("mode", "gateway")
	req.PostForm.Set("confluence_get_page", "on")
	if err = a.applyForm(req, 21); err != nil {
		t.Fatal(err)
	}
	restored, err := loadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if configRevision(restored.Products["jira"]) != configRevision(j) || !enabled(restored, "confluence", "confluence_get_page") {
		t.Fatal("потеря независимости модулей")
	}
	if _, err = a.reader(restored, "confluence"); err == nil {
		t.Fatal("отсутствие профиля должно блокировать внешний режим")
	}
}

func TestExpiredGatewaySession(t *testing.T) {
	var reject atomic.Bool
	var reads atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if reject.Load() {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<html>LogonPoint</html>")
			return
		}
		fmt.Fprint(w, `{"name":"fixture"}`)
	}))
	defer product.Close()
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logins := 0
	a.gateway.login = func(context.Context, string, gatewaySettings, string) (http.CookieJar, error) {
		logins++
		if logins == 2 {
			reject.Store(false)
		}
		return cookiejar.New(nil)
	}
	token, err := protect([]byte("fixture-pat"), true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaults()
	cfg.Gateway = gatewaySettings{URL: "https://gateway.example", User: "fixture", Password: []byte{1}, Seed: []byte{1}}
	cfg.Products["jira"] = productSettings{BaseURL: product.URL, Mode: "gateway", ProtectedPAT: token}
	read, err := a.reader(cfg, "jira")
	if err != nil {
		t.Fatal(err)
	}
	call := func() error {
		_, err := read(context.Background(), "/rest/api/2/myself", nil)
		return err
	}
	if err = call(); err != nil || logins != 1 || reads.Load() != 1 {
		t.Fatalf("первая сессия: %v, входы %d, запросы %d", err, logins, reads.Load())
	}
	reject.Store(true)
	if err = call(); err != nil || logins != 2 || reads.Load() != 3 {
		t.Fatalf("обновление истекшей сессии: %v, входы %d, запросы %d", err, logins, reads.Load())
	}
	reject.Store(true)
	if err = call(); err == nil || logins != 3 || reads.Load() != 5 {
		t.Fatalf("отказ новой сессии: %v, входы %d, запросы %d", err, logins, reads.Load())
	}
}
