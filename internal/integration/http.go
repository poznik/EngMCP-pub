package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Reader привязан к одному подключению. Модуль указывает только маршрут REST API и параметры чтения.
type Reader func(context.Context, string, url.Values) (Result, error)

func BaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("Укажи базовый HTTPS-адрес без логина и параметров.")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return "", errors.New("Требуется HTTPS. HTTP доступен для теста на loopback.")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func ProductReader(base, token string, jar http.CookieJar) Reader {
	return JSONReader(base, "/rest/", jar, false, func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) })
}

func HTTPClient(jar http.CookieJar) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// JSONReader принимает только маршруты, сформированные операциями зарегистрированного модуля.
func JSONReader(base, prefix string, jar http.CookieJar, allowArray bool, authorize func(*http.Request)) Reader {
	client := HTTPClient(jar)
	return func(ctx context.Context, path string, params url.Values) (Result, error) {
		if !strings.HasPrefix(path, prefix) || strings.Contains(path, "//") || strings.ContainsAny(path, "?#") {
			return Result{Stage: "validation"}, errors.New("Недопустимый маршрут API.")
		}
		uri := base + path
		if len(params) > 0 {
			uri += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return Result{Stage: "validation"}, errors.New("Неверный адрес сервиса.")
		}
		authorize(req)
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return Result{Stage: "network"}, ctx.Err()
			}
			return Result{Stage: "network"}, errors.New("Сервис недоступен. Проверь адрес, сеть и сертификат.")
		}
		defer response.Body.Close()
		res := Result{HTTPStatus: response.StatusCode, Stage: "http"}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			if u, e := response.Location(); e == nil {
				res.RedirectOrigin = u.Scheme + "://" + u.Host
			}
			if prefix == "/rest/" {
				return res, fmt.Errorf("Сервис вернул HTTP %d с перенаправлением. Если нужен вход через NetScaler, проверь режим подключения.", res.HTTPStatus)
			}
			return res, fmt.Errorf("Сервис вернул перенаправление HTTP %d. Проверь базовый адрес.", res.HTTPStatus)
		}
		isHTML := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html")
		if isHTML && (res.HTTPStatus == http.StatusUnauthorized || res.HTTPStatus == http.StatusForbidden) {
			head, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			res.Bytes = len(head)
			res.LoginPage = gatewayLoginPage(head)
			return res, fmt.Errorf("Сервис вернул HTML вместо данных API (HTTP %d). Проверь авторизацию.", res.HTTPStatus)
		}
		if response.StatusCode != 200 {
			return res, fmt.Errorf("Сервис вернул HTTP %d.", res.HTTPStatus)
		}
		res.Stage = "response"
		const limit = 32 << 20
		body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		res.Bytes = len(body)
		if err != nil || len(body) > limit {
			return res, errors.New("Ответ прерван или превышает 32 МиБ.")
		}
		trimmed := bytes.TrimSpace(body)
		validShape := bytes.HasPrefix(trimmed, []byte("{")) || (allowArray && bytes.HasPrefix(trimmed, []byte("[")))
		if !json.Valid(body) || !validShape {
			res.LoginPage = isHTML && gatewayLoginPage(body)
			return res, errors.New("Сервис вернул ответ в неожиданном формате. Проверь авторизацию.")
		}
		res.JSON = body
		return res, nil
	}
}
func gatewayLoginPage(body []byte) bool {
	head := bytes.ToLower(body[:min(len(body), 4096)])
	return bytes.Contains(head, []byte("logonpoint")) || bytes.Contains(head, []byte("/vpn/index.html"))
}

func User(ctx context.Context, read Reader, path string) (string, Result, error) {
	r, err := read(ctx, path, nil)
	if err != nil {
		return "", r, err
	}
	var user struct {
		DisplayName, Name, Username, Login, Type string
		Authenticated                            *bool
		Anonymous                                bool
	}
	if json.Unmarshal(r.JSON, &user) != nil || user.Anonymous || strings.EqualFold(user.Type, "anonymous") || (user.Authenticated != nil && !*user.Authenticated) {
		return "", r, errors.New("Не удалось подтвердить учётную запись сервиса.")
	}
	for _, s := range []string{user.DisplayName, user.Name, user.Username, user.Login} {
		if s != "" {
			return s, r, nil
		}
	}
	return "", r, errors.New("Сервис не вернул имя учётной записи.")
}
