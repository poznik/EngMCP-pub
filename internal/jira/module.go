package jira

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

func Module() integration.Module {
	m := integration.Module{ID: "jira", SupportsGateway: true, Title: "Jira Data Center", Check: func(ctx context.Context, read integration.Reader) (string, Result, error) {
		return integration.User(ctx, read, "/rest/api/2/myself")
	}}
	for _, op := range operations {
		props := map[string]any{op.Argument: map[string]any{"type": "string", "minLength": 1}}
		if op.Search {
			props["start_at"] = map[string]any{"type": "integer", "minimum": 0}
			props["max_results"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
		}
		desc := integration.Operation{Name: op.Name, Title: op.Title, Description: op.Description, ReadOnly: true,
			Schema: map[string]any{"type": "object", "properties": props, "required": []string{op.Argument}, "additionalProperties": false}}
		desc.Call = func(ctx context.Context, read integration.Reader, raw json.RawMessage) (integration.Result, error) {
			var args Arguments
			var params map[string]json.RawMessage
			invalid := errors.New("Параметры не соответствуют схеме инструмента.")
			if json.Unmarshal(raw, &params) != nil || params[op.Argument] == nil || json.Unmarshal(raw, &args) != nil {
				return Result{Stage: "validation"}, invalid
			}
			for k, v := range params {
				if _, ok := props[k]; !ok || string(v) == "null" {
					return Result{Stage: "validation"}, invalid
				}
			}
			return Call(ctx, read, op, args)
		}
		m.Operations = append(m.Operations, desc)
	}
	return m
}
