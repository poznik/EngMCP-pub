package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/poznik/EngMCP-pub/internal/gitlab"
	"github.com/poznik/EngMCP-pub/internal/integration"
	"github.com/poznik/EngMCP-pub/internal/jenkins"
	"golang.org/x/sys/windows"
)

type loginFunc func(context.Context, string, gatewaySettings, string) (http.CookieJar, error)
type cachedSession struct {
	key string
	jar http.CookieJar
}
type gatewayPool struct {
	mu        sync.Mutex
	sessions  map[string]cachedSession
	loginSlot chan struct{}
	login     loginFunc
}

func newGatewayPool() *gatewayPool {
	return &gatewayPool{sessions: map[string]cachedSession{}, loginSlot: make(chan struct{}, 1), login: loginGateway}
}
func (p *gatewayPool) cached(id, key string) http.CookieJar {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[id]; s.key == key {
		return s.jar
	}
	return nil
}
func (p *gatewayPool) clear(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == "gateway" {
		clear(p.sessions)
	} else {
		delete(p.sessions, id)
	}
}
func (p *gatewayPool) expire(id string, jar http.CookieJar) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions[id].jar == jar {
		delete(p.sessions, id)
	}
}
func (p *gatewayPool) session(ctx context.Context, id, key, base string, g gatewaySettings, dir string) (http.CookieJar, error) {
	if j := p.cached(id, key); j != nil {
		return j, nil
	}
	select {
	case p.loginSlot <- struct{}{}:
		defer func() { <-p.loginSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if j := p.cached(id, key); j != nil {
		return j, nil
	}
	jar, err := p.login(ctx, base, g, dir)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.sessions[id] = cachedSession{key, jar}
	p.mu.Unlock()
	return jar, nil
}
func (a *app) reader(cfg settings, id string) (integration.Reader, error) {
	product := cfg.Products[id]
	pat, err := protect(product.ProtectedPAT, false)
	if err != nil {
		return nil, err
	}
	if product.BaseURL == "" || len(pat) == 0 {
		return nil, errors.New("Сначала сохрани адрес сервиса и PAT.")
	}
	if product.Mode == "direct" {
		switch id {
		case "gitlab":
			return gitlab.Reader(product.BaseURL, string(pat)), nil
		case "jenkins":
			return jenkins.Reader(product.BaseURL, product.Username, string(pat)), nil
		case "allure":
			a.mu.RLock()
			session := a.allureTokens
			a.mu.RUnlock()
			return session.Reader(product.BaseURL, string(pat), configRevision(struct {
				Base   string
				Secret []byte
			}{product.BaseURL, product.ProtectedPAT})), nil
		}
		return integration.ProductReader(product.BaseURL, string(pat), nil), nil
	}
	g := cfg.Gateway
	if !g.ready() {
		return nil, errors.New("Заполни профиль на вкладке «Шлюз 2FA».")
	}
	guard := filepath.Join(a.dir, "gateway", configRevision(g))
	key := configRevision(struct{ Base, Profile string }{product.BaseURL, guard})
	return func(ctx context.Context, path string, q url.Values) (integration.Result, error) {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
		defer cancel()
		previous := a.gateway.cached(id, key)
		for {
			jar, err := a.gateway.session(ctx, id, key, product.BaseURL, g, guard)
			if err != nil {
				return integration.Result{Stage: "gateway"}, err
			}
			res, err := integration.ProductReader(product.BaseURL, string(pat), jar)(ctx, path, q)
			expired := res.LoginPage || (res.RedirectOrigin != "" && strings.EqualFold(res.RedirectOrigin, g.URL))
			if !expired {
				return res, err
			}
			a.gateway.expire(id, jar)
			if previous != jar {
				return res, errors.New("Шлюз не принял новую сессию сервиса. Проверь параметры подключения.")
			}
			previous = nil
		}
	}, nil
}

func gatewayHelperPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "engmcp-gateway", "engmcp-gateway.exe"), nil
}

func hasGatewayHelper() bool {
	path, err := gatewayHelperPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func loginGateway(ctx context.Context, base string, g gatewaySettings, dir string) (http.CookieJar, error) {
	helper, err := gatewayHelperPath()
	if err != nil {
		return nil, err
	}
	return runGatewayHelper(ctx, helper, base, g, dir)
}
func runGatewayHelper(ctx context.Context, helper, base string, g gatewaySettings, dir string) (http.CookieJar, error) {
	password, err := protect(g.Password, false)
	if err != nil {
		return nil, err
	}
	seed, err := protect(g.Seed, false)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(helper); err != nil {
		return nil, errors.New("Компонент NetScaler отсутствует. Установи совместимый компонент или выбери прямое подключение.")
	}
	input, err := json.Marshal(map[string]string{"product": base, "gateway": g.URL, "user": g.User, "password": string(password), "seed": string(seed), "guard_dir": dir})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper)
	cmd.Dir = filepath.Dir(helper)
	cmd.Stdin = bytes.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	// Буфер stderr не нужен: подробности HTTP и ответы шлюза не входят в аудит.
	var out bytes.Buffer
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Не удалось запустить компонент NetScaler.")
	}
	var reply struct {
		Error    string
		Category string
		Cookies  []struct {
			Name, Value, Path string
			Secure            bool
			Expires           int64 `json:"expires_unix"`
		}
	}
	if json.Unmarshal(out.Bytes(), &reply) != nil {
		return nil, errors.New("Не удалось прочитать ответ компонента NetScaler.")
	}
	switch reply.Error {
	case "":
	case "password":
		return nil, errors.New("Шлюз отклонил логин или пароль. Проверь профиль 2FA.")
	case "totp":
		return nil, errors.New("Шлюз отклонил одноразовый код. Проверь TOTP-секрет и часы компьютера.")
	case "blocked":
		return nil, errors.New("Вход остановлен после неудачных попыток. Исправь учётные данные в профиле 2FA.")
	case "configuration":
		return nil, fmt.Errorf("Компонент NetScaler не смог обработать профиль (%s).", reply.Category)
	case "network":
		return nil, errors.New("Нет соединения с сервисом или шлюзом. Проверь сеть и доверие к сертификату.")
	default:
		return nil, errors.New("Шлюз не подтвердил сессию. Проверь адреса и параметры входа.")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	var cookies []*http.Cookie
	for _, c := range reply.Cookies {
		cookie := &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure}
		if c.Expires > 0 {
			cookie.Expires = time.Unix(c.Expires, 0)
		}
		if cookie.Valid() == nil {
			cookies = append(cookies, cookie)
		}
	}
	if len(cookies) == 0 {
		return nil, errors.New("Шлюз не выдал сессию для хоста сервиса. Проверь выбранный режим и адрес.")
	}
	jar.SetCookies(u, cookies)
	return jar, nil
}
