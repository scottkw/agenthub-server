package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoot_AuthSignupVerifyLoginReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal shutdown differs on Windows; core auth is unit-tested")
	}

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18182",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_FROM=AgentHub <noreply@test.local>",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18182/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18182/api/auth/reset",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	base := "http://127.0.0.1:18182"
	waitReady(t, base+"/healthz")

	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email":        "e2e@example.com",
		"password":     "topsecretpw",
		"account_name": "E2E",
	}, 200)

	verifyToken := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": verifyToken}, 200)

	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))
	require.NotEmpty(t, login.Token)

	req, _ := http.NewRequest("POST", base+"/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()

	_ = postExpect(t, base+"/api/auth/reset-request", map[string]string{"email": "e2e@example.com"}, 200)
	resetToken := smtp.WaitForToken(t, "/api/auth/reset", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/reset", map[string]string{"token": resetToken, "password": "newpassword9"}, 200)

	// Old pw fails.
	_ = postExpect(t, base+"/api/auth/login", map[string]string{"email": "e2e@example.com", "password": "topsecretpw"}, 401)
	// New pw works.
	_ = postExpect(t, base+"/api/auth/login", map[string]string{"email": "e2e@example.com", "password": "newpassword9"}, 200)
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "server did not become ready")
}

func postExpect(t *testing.T, url string, body any, wantStatus int) []byte {
	t.Helper()
	bs, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(bs))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, wantStatus, resp.StatusCode, "body=%s", string(raw))
	return raw
}

// --- miniSMTP: accepts connections, reads DATA bodies into bodies chan,
// replies 250 to everything, closes cleanly on QUIT. ---

type miniSMTP struct {
	Port   string
	bodies chan string
}

func newMiniSMTP(t *testing.T) *miniSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	s := &miniSMTP{Port: port, bodies: make(chan string, 8)}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *miniSMTP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	w := conn
	write := func(line string) { _, _ = fmt.Fprintf(w, "%s\r\n", line) }

	write("220 mini.smtp")
	var body strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				s.bodies <- body.String()
				body.Reset()
				write("250 OK")
				inData = false
				continue
			}
			body.WriteString(line + "\n")
			continue
		}
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			write("250-mini.smtp")
			write("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(up, "AUTH"):
			write("235 auth ok")
		case strings.HasPrefix(up, "MAIL FROM"), strings.HasPrefix(up, "RCPT TO"):
			write("250 OK")
		case strings.HasPrefix(up, "DATA"):
			write("354 send it")
			inData = true
		case strings.HasPrefix(up, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func (s *miniSMTP) WaitForToken(t *testing.T, urlSubstring string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		select {
		case body := <-s.bodies:
			if !strings.Contains(body, urlSubstring) {
				continue
			}
			idx := strings.Index(body, "token=")
			require.NotEqual(t, -1, idx, "no token= in body: %s", body)
			tok := body[idx+len("token="):]
			if nl := strings.IndexAny(tok, " \r\n"); nl != -1 {
				tok = tok[:nl]
			}
			return strings.TrimSpace(tok)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("miniSMTP: no %q email within %s", urlSubstring, timeout)
		}
	}
}
