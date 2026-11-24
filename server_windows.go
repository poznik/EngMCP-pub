package main

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/poznik/EngMCP-pub/internal/allure"
	"github.com/poznik/EngMCP-pub/internal/integration"
)

const version = "0.3.0"

type app struct {
	dir          string
	mu           sync.RWMutex
	cfg          settings
	logs         journals
	server       *mcp.Server
	panel        *panel
	gateway      *gatewayPool
	allureTokens *allure.Session
}

func newApp(dir string) (*app, error) {
	cfg, err := loadSettings(dir)
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(dir, "logs")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		return nil, err
	}
	a := &app{allureTokens: &allure.Session{}, gateway: newGatewayPool(), dir: dir, cfg: cfg, logs: journals{dir: logDir}}
	if err = a.logs.prune(cfg.Retention); err != nil {
		return nil, err
	}
	a.server = mcp.NewServer(&mcp.Implementation{Name: "engmcp", Version: version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		Logger:       slog.New(slog.DiscardHandler),
	})
	a.server.AddReceivingMiddleware(a.enforce)
	a.publish(settings{}, cfg)
	return a, nil
}
func (a *app) snapshot() settings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}
func enabled(s settings, id, name string) bool {
	p := s.Products[id]
	return p.Enabled && p.Allowed[name]
}

func (a *app) enforce(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok || method != "tools/call" {
			return next(ctx, method, req)
		}
		cfg := a.snapshot()
		opName := "unknown_tool"
		allowed := false
		for id, m := range modules {
			for _, op := range m.Operations {
				if op.Name == call.Params.Name {
					opName = op.Name
					allowed = enabled(cfg, id, opName)
					break
				}
			}
		}
		if !allowed {
			if err := a.logs.write("audit", event{ID: randomID(), Action: opName, Result: "denied"}); err != nil {
				return nil, errors.New("Журнал аудита недоступен.")
			}
			return toolError("Операция недоступна по настройкам EngMCP."), nil
		}
		return next(ctx, method, req)
	}
}
func toolError(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (a *app) publish(old, cfg settings) {
	for id, module := range modules {
		for _, op := range module.Operations {
			if enabled(old, id, op.Name) == enabled(cfg, id, op.Name) {
				continue
			}
			if !enabled(cfg, id, op.Name) {
				a.server.RemoveTools(op.Name)
				continue
			}
			tool := &mcp.Tool{Name: op.Name, Description: op.Description, InputSchema: op.Schema,
				Annotations: &mcp.ToolAnnotations{Title: op.Title, ReadOnlyHint: op.ReadOnly, IdempotentHint: op.ReadOnly}}
			a.server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				cfg := a.snapshot()
				if !enabled(cfg, id, op.Name) {
					return a.finish(op.Name, time.Now(), integration.Result{}, errors.New("Операция отключена."))
				}
				read, err := a.reader(cfg, id)
				if err != nil {
					return a.finish(op.Name, time.Now(), integration.Result{}, err)
				}
				start := time.Now()
				res, err := op.Call(ctx, read, req.Params.Arguments)
				return a.finish(op.Name, start, res, err)
			})
		}
	}
}

func (a *app) finish(name string, start time.Time, r integration.Result, err error) (*mcp.CallToolResult, error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	e := event{ID: randomID(), Action: name, Result: outcome, HTTP: r.HTTPStatus, Bytes: r.Bytes, Stage: r.Stage, Ms: time.Since(start).Milliseconds()}
	if logErr := a.logs.write("diagnostic", e); logErr != nil {
		return nil, errors.New("Журнал диагностики недоступен.")
	}
	if logErr := a.logs.write("audit", e); logErr != nil {
		return nil, errors.New("Журнал аудита недоступен.")
	}
	if err != nil {
		return toolError(fmt.Sprintf("%s Запрос %s.", err, e.ID)), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(r.JSON)}}}, nil
}

func (a *app) applyForm(r *http.Request, days int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.cfg
	v := old
	if r.PostFormValue("revision") != configRevision(old) {
		return errors.New("Настройки изменились в другой вкладке. Обнови страницу.")
	}
	id := r.PostFormValue("service")
	if id == "" {
		id = "jira"
	}
	var changes []string
	var err error
	if id == "gateway" {
		v.Gateway, changes, err = gatewayForm(r, old.Gateway)
	} else if m, ok := modules[id]; ok {
		v.Products = maps.Clone(old.Products)
		v.Products[id], changes, err = productForm(r, m, old.Products[id])
	} else {
		return errors.New("Неизвестный модуль.")
	}
	if err != nil {
		return err
	}
	v.Retention = days
	if days != old.Retention {
		changes = append(changes, fmt.Sprintf("retention_days=%d", days))
	}
	if err = saveSettings(a.dir, v); err != nil {
		return err
	}
	a.cfg = v
	if id == "allure" && configRevision(old.Products[id]) != configRevision(v.Products[id]) {
		a.allureTokens = &allure.Session{}
	}
	if id == "gateway" && configRevision(old.Gateway) != configRevision(v.Gateway) {
		a.gateway.clear(id)
	} else if before, after := old.Products[id], v.Products[id]; before.BaseURL != after.BaseURL || before.Mode != after.Mode || (before.Enabled && !after.Enabled) {
		a.gateway.clear(id)
	}
	a.publish(old, v)
	if err = a.logs.write("audit", event{Action: id + "_settings", Result: "saved", Changes: changes}); err != nil {
		return errors.New("Настройки сохранены, запись аудита недоступна.")
	}
	return a.logs.prune(days)
}

func productForm(r *http.Request, m integration.Module, old productSettings) (productSettings, []string, error) {
	v := old
	changes := []string{}
	base := strings.TrimSpace(r.PostFormValue("url"))
	var err error
	if base != "" {
		base, err = integration.BaseURL(base)
		if err != nil {
			return v, nil, err
		}
	}
	v.BaseURL = base
	v.Enabled = r.PostFormValue("enabled") == "on"
	if mode := r.PostFormValue("mode"); mode != "" {
		v.Mode = mode
	}
	if v.Mode != old.Mode {
		changes = append(changes, "mode="+v.Mode)
	}
	if base != old.BaseURL {
		v.ProtectedPAT = nil
		changes = append(changes, "address_changed")
	}
	if m.ID == "jenkins" {
		v.Username = strings.TrimSpace(r.PostFormValue("username"))
		if v.Username != old.Username {
			v.ProtectedPAT = nil
			changes = append(changes, "username_changed")
		}
	}
	pat := r.PostFormValue("pat")
	forget := r.PostFormValue("forget") == "on"
	if len(pat) > 8192 || (pat != "" && forget) {
		return v, nil, errors.New("Проверь введённый токен.")
	}
	if forget {
		v.ProtectedPAT = nil
	}
	if pat != "" {
		v.ProtectedPAT, err = protect([]byte(pat), true)
		if err != nil {
			return v, nil, err
		}
		changes = append(changes, "pat_replaced")
	} else if len(v.ProtectedPAT) == 0 && len(old.ProtectedPAT) > 0 {
		changes = append(changes, "pat_removed")
	}
	if v.Enabled != old.Enabled {
		changes = append(changes, fmt.Sprintf("enabled=%t", v.Enabled))
	}
	v.Allowed = map[string]bool{}
	for _, op := range m.Operations {
		allow := r.PostFormValue(op.Name) == "on"
		v.Allowed[op.Name] = allow
		if old.Allowed[op.Name] != allow {
			changes = append(changes, fmt.Sprintf("%s=%t", op.Name, allow))
		}
	}
	return v, changes, nil
}

func gatewayForm(r *http.Request, old gatewaySettings) (gatewaySettings, []string, error) {
	if r.PostFormValue("forget") == "on" {
		return gatewaySettings{}, []string{"profile_removed"}, nil
	}
	v := old
	changes := []string{}
	endpoint, err := gatewayURL(r.PostFormValue("gateway_url"))
	if err != nil {
		return v, nil, err
	}
	user := strings.TrimSpace(r.PostFormValue("user"))
	if user == "" {
		return v, nil, errors.New("Укажи логин шлюза.")
	}
	if endpoint != old.URL || user != old.User {
		v = gatewaySettings{URL: endpoint, User: user}
		changes = append(changes, "identity_changed")
	}
	if pw := r.PostFormValue("password"); pw != "" {
		v.Password, err = protect([]byte(pw), true)
		if err != nil {
			return v, nil, err
		}
		changes = append(changes, "password_replaced")
	}
	if seed := r.PostFormValue("seed"); seed != "" {
		seed = strings.TrimRight(strings.ToUpper(strings.Join(strings.Fields(seed), "")), "=")
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seed)
		if err != nil || len(raw) < 14 {
			return v, nil, errors.New("Нужен TOTP-ключ из настройки аутентификатора: Base32, не менее 112 бит после декодирования.")
		}
		v.Seed, err = protect([]byte(seed), true)
		if err != nil {
			return v, nil, err
		}
		changes = append(changes, "totp_replaced")
	}
	if !v.ready() {
		return v, nil, errors.New("Введи пароль и TOTP-секрет для этой учётной записи и адреса шлюза.")
	}
	return v, changes, nil
}
