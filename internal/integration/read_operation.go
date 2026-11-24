package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Параметры используются одновременно для схемы MCP и проверки перед HTTP-вызовом.
type Parameter struct {
	Name, Description string
	Required          bool
	Integer           bool
	Min, Max          int64
	Default           string
}

func ID(name, description string) Parameter {
	return Parameter{Name: name, Description: description, Required: true, Integer: true, Min: 1}
}

func Page(name string, first int64) Parameter {
	return Parameter{Name: name, Description: "Номер страницы.", Integer: true, Min: first, Max: 2147483647, Default: strconv.FormatInt(first, 10)}
}

func Limit(name string) Parameter {
	return Parameter{Name: name, Description: "Размер страницы от 1 до 100.", Integer: true, Min: 1, Max: 100, Default: "25"}
}

type Route func(url.Values) (string, url.Values, error)

func ReadOperation(name, title, description string, fields []Parameter, route Route) Operation {
	props := map[string]any{}
	var required []string
	for _, f := range fields {
		prop := map[string]any{"type": "string", "description": f.Description}
		if f.Integer {
			prop["type"], prop["minimum"] = "integer", f.Min
			if f.Max > 0 {
				prop["maximum"] = f.Max
			}
			if f.Default != "" {
				n, _ := strconv.ParseInt(f.Default, 10, 64)
				prop["default"] = n
			}
		} else {
			prop["minLength"] = 1
		}
		props[f.Name] = prop
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	op := Operation{Name: name, Title: title, Description: description, ReadOnly: true, Schema: schema}
	op.Call = func(ctx context.Context, read Reader, raw json.RawMessage) (Result, error) {
		args, err := parseParameters(raw, fields)
		if err != nil {
			return Result{Stage: "validation"}, err
		}
		path, query, err := route(args)
		if err != nil {
			return Result{Stage: "validation"}, err
		}
		return read(ctx, path, query)
	}
	return op
}

func parseParameters(raw json.RawMessage, fields []Parameter) (url.Values, error) {
	inputs := map[string]json.RawMessage{}
	if len(raw) > 0 && (json.Unmarshal(raw, &inputs) != nil || inputs == nil) {
		return nil, fmt.Errorf("Ожидается объект параметров.")
	}
	args := url.Values{}
	for _, f := range fields {
		value, ok := inputs[f.Name]
		if !ok {
			if f.Required {
				return nil, fmt.Errorf("Укажи параметр %s.", f.Name)
			}
			if f.Default != "" {
				args.Set(f.Name, f.Default)
			}
			continue
		}
		delete(inputs, f.Name)
		if f.Integer {
			number, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil || number < f.Min || (f.Max > 0 && number > f.Max) {
				return nil, fmt.Errorf("Недопустимое число для %s.", f.Name)
			}
			args.Set(f.Name, strconv.FormatInt(number, 10))
		} else {
			var text string
			if json.Unmarshal(value, &text) != nil || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("Нужна непустая строка для %s.", f.Name)
			}
			args.Set(f.Name, text)
		}
	}
	if len(inputs) > 0 {
		return nil, fmt.Errorf("Передан неизвестный параметр.")
	}
	return args, nil
}
