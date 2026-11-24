// Package confluence читает страницы Confluence Data Center через REST API.
package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

var pageID = regexp.MustCompile("^[0-9]+$")

func Module() integration.Module {
	search := integration.Operation{Name: "confluence_search_cql", Title: "Поиск страниц по CQL", Description: "Выполнить CQL в Confluence. Результаты содержат страницу выдачи и её границы.", ReadOnly: true}
	search.Schema = map[string]any{"type": "object", "required": []string{"cql"}, "additionalProperties": false, "properties": map[string]any{"cql": map[string]any{"type": "string", "minLength": 1}, "start": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}
	search.Call = func(ctx context.Context, read integration.Reader, raw json.RawMessage) (integration.Result, error) {
		args := struct {
			CQL          string
			Start, Limit int
		}{Limit: 25}
		if !validArgs(raw, &args, "cql", "start", "limit") || strings.TrimSpace(args.CQL) == "" || args.Start < 0 || args.Limit < 1 || args.Limit > 100 {
			return integration.Result{Stage: "validation"}, errors.New("Укажи CQL, start ≥ 0 и limit от 1 до 100.")
		}
		return read(ctx, "/rest/api/content/search", url.Values{"cql": {args.CQL}, "start": {strconv.Itoa(args.Start)}, "limit": {strconv.Itoa(args.Limit)}})
	}
	page := integration.Operation{Name: "confluence_get_page", Title: "Содержимое страницы", Description: "Прочитать страницу Confluence по числовому ID, включая тело, версию и пространство.", ReadOnly: true}
	page.Schema = map[string]any{"type": "object", "required": []string{"id"}, "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "string", "pattern": "^[0-9]+$"}}}
	page.Call = func(ctx context.Context, read integration.Reader, raw json.RawMessage) (integration.Result, error) {
		var args struct{ ID string }
		if !validArgs(raw, &args, "id") || !pageID.MatchString(args.ID) {
			return integration.Result{Stage: "validation"}, errors.New("Нужен числовой ID страницы Confluence.")
		}
		return read(ctx, "/rest/api/content/"+args.ID, url.Values{"expand": {"body.storage,version,space"}})
	}
	return integration.Module{ID: "confluence", SupportsGateway: true, Title: "Confluence Data Center", Operations: []integration.Operation{search, page}, Check: func(ctx context.Context, read integration.Reader) (string, integration.Result, error) {
		return integration.User(ctx, read, "/rest/api/user/current")
	}}
}
func validArgs(raw json.RawMessage, dst any, fields ...string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || len(obj) == 0 {
		return false
	}
	for k, v := range obj {
		ok := false
		for _, f := range fields {
			if f == k {
				ok = true
				break
			}
		}
		if !ok || bytes.Equal(v, []byte("null")) {
			return false
		}
	}
	return json.Unmarshal(raw, dst) == nil
}
