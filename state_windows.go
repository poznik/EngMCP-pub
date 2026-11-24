package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/poznik/EngMCP-pub/internal/integration"
	"golang.org/x/sys/windows"
)

type productSettings struct {
	Username     string          `json:"username,omitempty"`
	Enabled      bool            `json:"enabled"`
	BaseURL      string          `json:"base_url"`
	ProtectedPAT []byte          `json:"dpapi_pat,omitempty"`
	Allowed      map[string]bool `json:"allowed"`
	Mode         string          `json:"mode"`
}
type gatewaySettings struct {
	URL      string `json:"url"`
	User     string `json:"user"`
	Password []byte `json:"dpapi_password,omitempty"`
	Seed     []byte `json:"dpapi_totp,omitempty"`
}
type settings struct {
	Version   int                        `json:"version"`
	Retention int                        `json:"retention_days"`
	Products  map[string]productSettings `json:"products"`
	Gateway   gatewaySettings            `json:"gateway"`
}

func (g gatewaySettings) ready() bool {
	return g.URL != "" && g.User != "" && len(g.Password) > 0 && len(g.Seed) > 0
}
func gatewayURL(raw string) (string, error) {
	base, err := integration.BaseURL(raw)
	if err == nil {
		u, _ := url.Parse(base)
		if u.Scheme == "https" && u.Path == "" {
			return base, nil
		}
	}
	return "", errors.New("Укажи HTTPS-адрес шлюза без пути, например https://mfa.example.org.")
}

func protect(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errors.New("Не удалось открыть хранилище DPAPI текущего пользователя.")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func defaults() settings {
	v := settings{Version: 2, Retention: 14, Products: map[string]productSettings{}}
	for id, m := range modules {
		p := productSettings{Mode: "direct", Allowed: map[string]bool{}}
		for _, op := range m.Operations {
			p.Allowed[op.Name] = true
		}
		v.Products[id] = p
	}
	return v
}
func (v settings) validate() error {
	if v.Version != 2 {
		return errors.New("Неизвестная версия настроек.")
	}
	if v.Retention < 1 || v.Retention > 3650 {
		return errors.New("Срок хранения должен быть от 1 до 3650 дней.")
	}
	if v.Gateway.URL != "" {
		if _, err := gatewayURL(v.Gateway.URL); err != nil {
			return err
		}
	}
	for id, m := range modules {
		p := v.Products[id]
		if p.Mode != "direct" && !(p.Mode == "gateway" && m.SupportsGateway) {
			return errors.New("Неизвестный режим подключения.")
		}
		if p.BaseURL != "" {
			if _, err := integration.BaseURL(p.BaseURL); err != nil {
				return err
			}
		}
		if p.Mode == "gateway" && p.BaseURL != "" && !strings.HasPrefix(p.BaseURL, "https://") {
			return errors.New("Для подключения через шлюз нужен HTTPS-адрес сервиса.")
		}
		if id == "jenkins" && (p.Enabled || len(p.ProtectedPAT) > 0) && (p.Username == "" || strings.ContainsAny(p.Username, ":\r\n")) {
			return errors.New("Укажи имя пользователя Jenkins без двоеточия.")
		}
		if p.Enabled && (p.BaseURL == "" || len(p.ProtectedPAT) == 0) {
			return fmt.Errorf("Для включения %s укажи адрес и PAT.", m.Title)
		}
	}
	return nil
}
func loadSettings(dir string) (settings, error) {
	v := defaults()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) != nil {
		return v, errors.New("Не удалось прочитать settings.json.")
	}
	switch header.Version {
	case 1:
		var legacy struct {
			productSettings
			Retention int `json:"retention_days"`
		}
		if err = json.Unmarshal(data, &legacy); err != nil {
			return v, err
		}
		legacy.Mode = "direct"
		v.Products["jira"] = legacy.productSettings
		v.Retention = legacy.Retention
	case 2:
		// Новые модули, отсутствующие в старом файле, остаются выключенными.
		if err = json.Unmarshal(data, &v); err != nil {
			return v, err
		}
	default:
		return v, errors.New("Неизвестная версия настроек.")
	}
	return v, v.validate()
}

func saveSettings(dir string, v settings) error {
	if err := v.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	src, _ := windows.UTF16PtrFromString(f.Name())
	dst, _ := windows.UTF16PtrFromString(filepath.Join(dir, "settings.json"))
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type event struct {
	PID     int       `json:"pid,omitempty"`
	Stage   string    `json:"stage,omitempty"`
	Time    time.Time `json:"time"`
	ID      string    `json:"request_id,omitempty"`
	Action  string    `json:"action"`
	Result  string    `json:"result"`
	HTTP    int       `json:"http_status,omitempty"`
	Bytes   int       `json:"bytes,omitempty"`
	Ms      int64     `json:"duration_ms"`
	Changes []string  `json:"changes,omitempty"`
}

type journals struct {
	dir string
	mu  sync.Mutex
}

func (j *journals) write(stream string, e event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	e.Time = time.Now().UTC()
	date := e.Time.Format(time.DateOnly)
	path := filepath.Join(j.dir, stream+"-"+date+".jsonl")
	if st, err := os.Stat(path); err == nil && st.Size() >= 5<<20 {
		rotated := strings.TrimSuffix(path, ".jsonl") + "-" + randomID() + ".jsonl"
		if err = os.Rename(path, rotated); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	ce := f.Close()
	if err != nil {
		return err
	}
	return ce
}

func (j *journals) prune(days int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	files, err := os.ReadDir(j.dir)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		suffix, ok := strings.CutPrefix(name, "audit-")
		if !ok {
			suffix, ok = strings.CutPrefix(name, "diagnostic-")
		}
		if !ok || len(suffix) < 11 {
			continue
		}
		date, err := time.Parse(time.DateOnly, suffix[:10])
		if err == nil && date.Before(cutoff) {
			if err = os.Remove(filepath.Join(j.dir, name)); err != nil {
				return fmt.Errorf("очистка журнала: %w", err)
			}
		}
	}
	return nil
}
