package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

//go:embed web/*
var assets embed.FS

func shortTitle(m integration.Module) string { return strings.TrimSuffix(m.Title, " Data Center") }

var page = template.Must(template.New("panel.html").Funcs(template.FuncMap{"shortTitle": shortTitle}).ParseFS(assets, "web/panel.html"))

type panel struct {
	app               *app
	host, path, csrf  string
	mu                sync.Mutex
	tickets, sessions map[string]time.Time
}

func newPanel(a *app, host string) *panel {
	return &panel{app: a, host: host, path: "/ui/" + randomID() + "/", csrf: randomID(), tickets: map[string]time.Time{}, sessions: map[string]time.Time{}}
}
func clean(m map[string]time.Time) {
	for k, t := range m {
		if !time.Now().Before(t) {
			delete(m, k)
		}
	}
}
func (p *panel) link() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	clean(p.tickets)
	t := randomID()
	p.tickets[t] = time.Now().Add(time.Minute)
	return "http://" + p.host + "/#" + t
}
func configRevision(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.Host != p.host {
		http.Error(w, "Недопустимый Host", 403)
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("Origin") != "http://"+p.host {
			http.Error(w, "Недопустимый Origin", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Некорректная форма", 400)
			return
		}
	}
	if r.Method == http.MethodGet {
		files := map[string]string{"/": "login.html", "/login.js": "login.js", "/panel.js": "panel.js", "/style.css": "style.css"}
		if f, ok := files[r.URL.Path]; ok {
			mime := map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css"}
			data, _ := assets.ReadFile("web/" + f)
			w.Header().Set("Content-Type", mime[filepath.Ext(f)])
			_, _ = w.Write(data)
			return
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == "/login" {
		p.mu.Lock()
		clean(p.tickets)
		clean(p.sessions)
		t := r.PostFormValue("ticket")
		_, ok := p.tickets[t]
		delete(p.tickets, t)
		if !ok {
			p.mu.Unlock()
			http.Error(w, "Ссылка истекла. Открой панель из ярлыка ещё раз.", 403)
			return
		}
		sid := randomID()
		p.sessions[sid] = time.Now().Add(8 * time.Hour)
		p.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "engmcp_session", Value: sid, Path: p.path, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
		http.Redirect(w, r, p.path, http.StatusSeeOther)
		return
	}
	if !strings.HasPrefix(r.URL.Path, p.path) {
		http.NotFound(w, r)
		return
	}
	cookie, err := r.Cookie("engmcp_session")
	p.mu.Lock()
	valid := err == nil && time.Now().Before(p.sessions[cookieValue(cookie)])
	p.mu.Unlock()
	if !valid {
		http.Error(w, "Открой панель из ярлыка EngMCP.", 403)
		return
	}
	if r.URL.Path == p.path && r.URL.Query().Get("service") == "gateway" && !hasGatewayHelper() {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == p.path {
		p.render(w, selectedService(r.URL.Query().Get("service")), 200, r.URL.Query().Get("saved") == "1", "", "")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Метод недоступен", 405)
		return
	}
	if subtle.ConstantTimeCompare([]byte(p.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		http.Error(w, "Обнови страницу", 403)
		return
	}
	id := selectedService(r.PostFormValue("service"))
	switch r.URL.Path {
	case p.path + "save":
		if !hasGatewayHelper() {
			if id == "gateway" {
				http.NotFound(w, r)
				return
			}
			if r.PostFormValue("mode") == "gateway" && p.app.snapshot().Products[id].Mode != "gateway" {
				p.render(w, id, 400, false, "Для этого режима требуется отдельный компонент NetScaler.", "")
				return
			}
		}
		days, _ := strconv.Atoi(r.PostFormValue("days"))
		if err := p.app.applyForm(r, days); err != nil {
			p.render(w, id, 400, false, err.Error(), "")
			return
		}
		http.Redirect(w, r, p.path+"?saved=1&service="+id, 303)
	case p.path + "check":
		cfg := p.app.snapshot()
		module, ok := modules[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		read, err := p.app.reader(cfg, id)
		if err != nil {
			p.render(w, id, 400, false, err.Error(), "")
			return
		}
		start := time.Now()
		name, result, err := module.Check(r.Context(), read)
		_, logErr := p.app.finish(module.ID+"_check_connection", start, result, err)
		if logErr != nil {
			p.render(w, id, 500, false, logErr.Error(), "")
			return
		}
		if err != nil {
			p.render(w, id, 502, false, err.Error(), "")
			return
		}
		p.render(w, id, 200, false, "", "Подключено. Учётная запись — "+name)
	default:
		http.NotFound(w, r)
	}
}
func cookieValue(c *http.Cookie) string {
	if c == nil {
		return ""
	}
	return c.Value
}
func selectedService(id string) string {
	if _, known := modules[id]; id == "gateway" || known {
		return id
	}
	return "jira"
}

func (p *panel) render(w http.ResponseWriter, id string, status int, saved bool, failure, notice string) {
	cfg := p.app.snapshot()
	exe, _ := os.Executable()
	exe = filepath.ToSlash(exe)
	quoted, _ := json.Marshal(exe)
	codex := "[mcp_servers.engmcp]\ncommand = " + string(quoted) + "\nargs = [\"bridge\"]\ntool_timeout_sec = 120"
	dsh := "- id: mcp-engmcp\n  name: '@deepseek-ai/dsh-mcp-client'\n  config:\n    serverName: engmcp\n    transport: stdio\n    command: " + string(quoted) + "\n    args: [bridge]\n    toolCallTimeoutMs: 120000"
	claude := fmt.Sprintf(`{
  "mcpServers": {
    "engmcp": {
      "command": %s,
      "args": ["bridge"]
    }
  }
}`, quoted)
	product := cfg.Products[id]
	module := modules[id]
	data := map[string]any{"Catalogue": catalogue, "ShortTitle": shortTitle(module), "SupportsGateway": module.SupportsGateway, "HasGateway": hasGatewayHelper(), "Cfg": product, "Gateway": cfg.Gateway, "Retention": cfg.Retention, "Service": id, "Revision": configRevision(cfg), "ModuleTitle": module.Title, "Ops": module.Operations, "CSRF": p.csrf, "HasPAT": len(product.ProtectedPAT) > 0, "Version": version, "Saved": saved, "Failure": failure, "Notice": notice, "Codex": codex, "DSH": dsh, "Claude": claude, "Logs": p.app.logs.dir, "Path": p.path}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = page.ExecuteTemplate(w, "panel.html", data)
}

func validPanelLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/" && len(u.Fragment) == 48
}
