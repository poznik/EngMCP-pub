package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveForm(a *app, base, pat string, enable bool) *http.Request {
	form := url.Values{"revision": {configRevision(a.snapshot())}, "url": {base}, "pat": {pat}}
	if enable {
		form.Set("enabled", "on")
	}
	req := httptest.NewRequest("POST", "/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
func TestSettingsAndTokenOrigin(t *testing.T) {
	dir := t.TempDir()
	a, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.applyForm(saveForm(a, "https://one.example/jira", "marker-private", true), 7); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || bytes.Contains(disk, []byte("marker-private")) {
		t.Fatal("PAT в открытом виде")
	}
	restored, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := restored.snapshot()
	plain, err := protect(v.Products["jira"].ProtectedPAT, false)
	if err != nil || string(plain) != "marker-private" || !v.Products["jira"].Enabled || v.Retention != 7 {
		t.Fatal("перезагрузка конфигурации")
	}
	if err = a.applyForm(saveForm(a, "https://two.example", "", true), 5); err == nil {
		t.Fatal("смена адреса унаследовала PAT")
	}
	if a.snapshot().Products["jira"].BaseURL != v.Products["jira"].BaseURL {
		t.Fatal("частичное сохранение")
	}
	if err = a.applyForm(saveForm(a, "https://two.example", "", false), 5); err != nil || len(a.snapshot().Products["jira"].ProtectedPAT) != 0 {
		t.Fatal("старый PAT должен быть отвязан")
	}
}
func TestLogRetention(t *testing.T) {
	j := journals{dir: t.TempDir()}
	old := time.Now().UTC().AddDate(0, 0, -10).Format(time.DateOnly)
	expired := filepath.Join(j.dir, "audit-"+old+"-rolled.jsonl")
	if err := os.WriteFile(expired, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.write("audit", event{Action: "test", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := j.prune(1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatal("старый журнал остался")
	}
	current, _ := filepath.Glob(filepath.Join(j.dir, "audit-*.jsonl"))
	if len(current) != 1 {
		t.Fatal("текущий журнал удалён")
	}
}
