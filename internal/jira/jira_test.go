package jira

import (
	"context"
	"encoding/json"
	"github.com/poznik/EngMCP-pub/internal/integration"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryAndProjection(t *testing.T) {
	received := make(chan *http.Request, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()
	tests := []struct {
		index             int
		args              Arguments
		path, jql, fields string
	}{
		{0, Arguments{Text: `a "b"\c`}, "/jira/rest/api/2/search", `text ~ "a \"b\"\\c" ORDER BY updated DESC`, "summary,status,issuetype,updated"},
		{1, Arguments{JQL: "filter = 12 ORDER BY key", Start: 5}, "/jira/rest/api/2/search", "filter = 12 ORDER BY key", "summary,status,issuetype,updated"},
		{2, Arguments{Key: "AB-1"}, "/jira/rest/api/2/issue/AB-1", "", "summary,description,status,assignee,reporter,issuetype,priority,created,updated,labels,issuelinks"},
		{3, Arguments{Key: "AB-1"}, "/jira/rest/api/2/issue/AB-1", "", "*all"},
	}
	for _, tc := range tests {
		_, err := Call(context.Background(), integration.ProductReader(api.URL+"/jira", "test-token", nil), operations[tc.index], tc.args)
		if err != nil {
			t.Fatal(err)
		}
		r := <-received
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != tc.path || q.Get("jql") != tc.jql || q.Get("fields") != tc.fields || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("неверный запрос операции %s", operations[tc.index].Name)
		}
		if tc.index == 1 && q.Get("startAt") != "5" {
			t.Fatal("смещение потеряно")
		}
		if tc.index == 3 && q.Get("expand") != "names,schema" {
			t.Fatal("метаданные полей потеряны")
		}
	}
}
func TestRedirectAndArguments(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("переход на другой адрес") }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	_, res, err := Module().Check(context.Background(), integration.ProductReader(origin.URL, "test", nil))
	if err == nil || res.HTTPStatus != 302 {
		t.Fatal("переадресация должна вернуть ошибку")
	}
	for _, args := range []string{`{"key":"DEMO-2","fields":"*all"}`, `{"key":null}`, `{}`, `{"key":"../myself"}`} {
		res, err := Module().Operations[2].Call(context.Background(), integration.ProductReader(origin.URL, "test", nil), json.RawMessage(args))
		if err == nil || res.HTTPStatus != 0 {
			t.Fatal("ожидался локальный отказ", args)
		}
	}
}
