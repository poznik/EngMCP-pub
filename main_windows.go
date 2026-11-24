package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/poznik/EngMCP-pub/internal/allure"
	"github.com/poznik/EngMCP-pub/internal/confluence"
	"github.com/poznik/EngMCP-pub/internal/gitlab"
	"github.com/poznik/EngMCP-pub/internal/integration"
	"github.com/poznik/EngMCP-pub/internal/jenkins"
	"github.com/poznik/EngMCP-pub/internal/jira"
	"golang.org/x/sys/windows"
)

var catalogue = []integration.Module{jira.Module(), confluence.Module(), gitlab.Module(), jenkins.Module(), allure.Module()}
var modules = func() map[string]integration.Module {
	items := make(map[string]integration.Module, len(catalogue))
	for _, m := range catalogue {
		items[m.ID] = m
	}
	return items
}()

type location struct{ dir, pipe, sid string }

func locate() (location, error) {
	var l location
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return l, err
	}
	l.sid = u.User.Sid.String()
	dir := os.Getenv("ENGMCP_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "EngMCP")
	}
	l.dir, err = filepath.Abs(dir)
	if err != nil {
		return l, err
	}
	hash := sha256.Sum256([]byte(strings.ToLower(l.dir)))
	l.pipe = fmt.Sprintf(`\\.\pipe\engmcp-%s-%x`, l.sid, hash[:8])
	return l, nil
}
func (l location) dial(ctx context.Context) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, l.pipe)
	if err != nil {
		return nil, err
	}
	fd, ok := conn.(interface{ Fd() uintptr })
	if ok {
		sd, e := windows.GetSecurityInfo(windows.Handle(fd.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if e == nil {
			owner, _, e := sd.Owner()
			if e == nil && owner.String() == l.sid {
				return conn, nil
			}
		}
	}
	conn.Close()
	return nil, errors.New("Владелец канала EngMCP не совпадает с текущим пользователем.")
}
func (l location) connect(start bool) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	conn, err := l.dial(ctx)
	if err == nil || !start || !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return conn, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	for ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		conn, err = l.dial(ctx)
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, err
		}
	}
	return nil, errors.New("Истекло время запуска EngMCP.")
}
func (l location) serve() error {
	ln, err := winio.ListenPipe(l.pipe, &winio.PipeConfig{SecurityDescriptor: "O:" + l.sid + "D:P(A;;GA;;;" + l.sid + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return err
	}
	defer ln.Close()
	a, err := newApp(l.dir)
	if err != nil {
		return err
	}
	httpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	a.panel = newPanel(a, httpLn.Addr().String())
	h := &http.Server{Handler: a.panel, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 110 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = a.logs.write("diagnostic", event{Action: "server", Result: "started", PID: os.Getpid()}); err != nil {
		httpLn.Close()
		return err
	}
	go func() { _ = h.Serve(httpLn); cancel() }()
	go func() { <-ctx.Done(); ln.Close(); h.Close() }()
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := a.logs.prune(a.snapshot().Retention); err != nil {
					_ = a.logs.write("diagnostic", event{Action: "retention", Result: "error"})
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				cancel()
				return err
			}
			break
		}
		wg.Go(func() {
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var cmd [1]byte
			if _, err := io.ReadFull(conn, cmd[:]); err != nil {
				return
			}
			conn.SetReadDeadline(time.Time{})
			switch cmd[0] {
			case 'M':
				_ = a.server.Run(ctx, &mcp.IOTransport{Reader: conn, Writer: conn})
			case 'U':
				_, _ = io.WriteString(conn, a.panel.link())
			case 'Q':
				cancel()
			}
		})
	}
	wg.Wait()
	return a.logs.write("diagnostic", event{Action: "server", Result: "stopped"})
}

func run() error {
	l, err := locate()
	if err != nil {
		return err
	}
	cmd := "open"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if len(os.Args) > 2 {
		return errors.New("Использование: engmcp [open|bridge|serve|stop|version]")
	}
	switch cmd {
	case "version":
		fmt.Println(version)
		return nil
	case "serve":
		return l.serve()
	case "open", "panel-url", "bridge", "stop":
	default:
		return errors.New("Неизвестная команда EngMCP.")
	}
	conn, err := l.connect(cmd != "stop")
	if err != nil {
		if cmd == "stop" && errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return err
	}
	defer conn.Close()
	if cmd == "bridge" {
		if _, err = conn.Write([]byte("M")); err != nil {
			return err
		}
		go func() { _, _ = io.Copy(conn, os.Stdin); conn.Close() }()
		_, err = io.Copy(os.Stdout, conn)
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	if cmd == "stop" {
		var pid uint32
		handle := windows.Handle(conn.(interface{ Fd() uintptr }).Fd())
		if err = windows.GetNamedPipeServerProcessId(handle, &pid); err != nil {
			return err
		}
		process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(process)
		if _, err = conn.Write([]byte("Q")); err != nil {
			return err
		}
		status, err := windows.WaitForSingleObject(process, 15000)
		if err != nil {
			return err
		}
		if status != windows.WAIT_OBJECT_0 {
			return errors.New("Не удалось дождаться остановки EngMCP.")
		}
		return nil
	}
	if _, err = conn.Write([]byte("U")); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(conn, 1024))
	if err != nil {
		return err
	}
	s := string(raw)
	if !validPanelLink(s) {
		return errors.New("Не удалось получить ссылку панели.")
	}
	if cmd == "panel-url" {
		fmt.Println(s)
		return nil
	}
	uri, _ := windows.UTF16PtrFromString(s)
	return windows.ShellExecute(0, nil, uri, nil, nil, windows.SW_SHOWNORMAL)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
