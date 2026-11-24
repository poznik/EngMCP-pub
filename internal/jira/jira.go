// Package jira реализует операции чтения Jira Data Center через REST API v2.
package jira

import (
	"context"

	"errors"

	"github.com/poznik/EngMCP-pub/internal/integration"

	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Operation struct {
	Name, Title, Description, Argument string
	Search                             bool
}

var operations = []Operation{
	{"jira_search_text", "Поиск по тексту", "Найти задачи по тексту. Возвращает страницу кратких результатов Jira.", "text", true},
	{"jira_search_jql", "Поиск по JQL", "Выполнить готовый JQL без изменения, включая filter = 12345. Для следующей страницы передать start_at.", "jql", true},
	{"jira_get_issue", "Содержимое задачи", "Прочитать основные поля задачи, включая описание, статус, исполнителя и связи.", "key", false},
	{"jira_get_all_fields", "Все поля задачи", "Прочитать все доступные поля Jira, включая customfield, их названия и схемы.", "key", false},
}

type Arguments struct {
	Text  string `json:"text"`
	JQL   string `json:"jql"`
	Key   string `json:"key"`
	Start int    `json:"start_at"`
	Limit *int   `json:"max_results"`
}
type Result = integration.Result

var keyPattern = regexp.MustCompile("^(?:[A-Za-z][A-Za-z0-9_]*-[0-9]+|[0-9]+)$")

func Call(ctx context.Context, read integration.Reader, op Operation, args Arguments) (Result, error) {
	q := url.Values{}
	path := "/rest/api/2/search"
	if op.Search {
		jql := args.JQL
		if op.Argument == "text" {
			if strings.TrimSpace(args.Text) == "" {
				return Result{Stage: "validation"}, errors.New("Нужен текст для поиска.")
			}
			escape := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\r", " ", "\n", " ")
			jql = "text ~ \"" + escape.Replace(args.Text) + "\" ORDER BY updated DESC"
		}
		limit := 50
		if args.Limit != nil {
			limit = *args.Limit
		}
		if strings.TrimSpace(jql) == "" || limit < 1 || limit > 100 || args.Start < 0 {
			return Result{Stage: "validation"}, errors.New("Проверь JQL, start_at ≥ 0 и max_results от 1 до 100.")
		}
		q.Set("jql", jql)
		q.Set("fields", "summary,status,issuetype,updated")
		q.Set("maxResults", strconv.Itoa(limit))
		q.Set("startAt", strconv.Itoa(args.Start))
	} else {
		if !keyPattern.MatchString(args.Key) {
			return Result{Stage: "validation"}, errors.New("Нужен ключ задачи вида DEMO-12 или числовой ID.")
		}
		path = "/rest/api/2/issue/" + args.Key
		if op.Name == "jira_get_all_fields" {
			q.Set("fields", "*all")
			q.Set("expand", "names,schema")
		} else {
			q.Set("fields", "summary,description,status,assignee,reporter,issuetype,priority,created,updated,labels,issuelinks")
		}
	}
	return read(ctx, path, q)
}
