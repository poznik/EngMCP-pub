package allure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

// Session хранит только краткоживущий Bearer в памяти. key обозначает адрес и ревизию сохранённого API-токена.
type Session struct {
	mu          sync.Mutex
	key, bearer string
	until       time.Time
}

func (s *Session) Reader(base, apiToken, key string) integration.Reader {
	return func(ctx context.Context, path string, q url.Values) (integration.Result, error) {
		retryCached := true
		for {
			bearer, cached, status, err := s.token(ctx, base, apiToken, key)
			if err != nil {
				return integration.Result{Stage: "auth", HTTPStatus: status}, err
			}
			read := integration.JSONReader(base, "/api/", nil, false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+bearer) })
			res, err := read(ctx, path, q)
			if res.HTTPStatus != http.StatusUnauthorized {
				return res, err
			}
			s.mu.Lock()
			if s.key == key && s.bearer == bearer {
				s.bearer = ""
			}
			s.mu.Unlock()
			if !cached || !retryCached {
				return res, err
			}
			retryCached = false
		}
	}
}

func (s *Session) token(ctx context.Context, base, apiToken, key string) (string, bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == key && s.bearer != "" && time.Now().Before(s.until) {
		return s.bearer, true, 0, nil
	}
	var body bytes.Buffer
	fields := multipart.NewWriter(&body)
	for _, f := range [][2]string{{"grant_type", "apitoken"}, {"scope", "openid"}, {"token", apiToken}} {
		if err := fields.WriteField(f[0], f[1]); err != nil {
			return "", false, 0, err
		}
	}
	if err := fields.Close(); err != nil {
		return "", false, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/uaa/oauth/token", &body)
	if err != nil {
		return "", false, 0, errors.New("Неверный адрес Allure TestOps.")
	}
	req.Header.Set("Content-Type", fields.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	res, err := integration.HTTPClient(nil).Do(req)
	if err != nil {
		return "", false, 0, errors.New("Не удалось соединиться с Allure TestOps для обмена API-токена.")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", false, res.StatusCode, fmt.Errorf("Обмен API-токена Allure TestOps вернул HTTP %d.", res.StatusCode)
	}
	const maxTokenReply = 64 << 10
	data, err := io.ReadAll(io.LimitReader(res.Body, maxTokenReply+1))
	var reply struct {
		Access  string `json:"access_token"`
		Expires int64  `json:"expires_in"`
		Type    string `json:"token_type"`
	}
	if err != nil || len(data) > maxTokenReply || json.Unmarshal(data, &reply) != nil || reply.Access == "" || strings.ContainsAny(reply.Access, " \t\r\n") || (reply.Type != "" && !strings.EqualFold(reply.Type, "bearer")) {
		return "", false, res.StatusCode, errors.New("Allure TestOps вернул некорректный ответ при обмене API-токена.")
	}
	// При отсутствии срока только текущий вызов использует полученный токен.
	ttl := max(0, min(reply.Expires, int64(86400)))
	s.key, s.bearer = key, reply.Access
	s.until = time.Now().Add(time.Duration(max(ttl-5, 0)) * time.Second)
	return reply.Access, false, res.StatusCode, nil
}
