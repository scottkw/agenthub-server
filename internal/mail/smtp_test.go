package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSMTPServer accepts one message, parses enough to verify the From/To,
// captures the DATA body, and closes.
type fakeSMTPServer struct {
	addr     string
	received chan fakeMsg
	done     chan struct{}
}

type fakeMsg struct {
	from string
	to   []string
	body string
}

func startFakeSMTP(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &fakeSMTPServer{
		addr:     ln.Addr().String(),
		received: make(chan fakeMsg, 1),
		done:     make(chan struct{}),
	}

	go func() {
		defer close(s.done)
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)

		write := func(s string) { fmt.Fprintf(w, "%s\r\n", s); _ = w.Flush() }

		write("220 fake.smtp")

		var msg fakeMsg
		inData := false
		var dataBuf strings.Builder

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")

			if inData {
				if line == "." {
					msg.body = dataBuf.String()
					write("250 OK")
					inData = false
					continue
				}
				dataBuf.WriteString(line + "\r\n")
				continue
			}

			switch {
			case strings.HasPrefix(strings.ToUpper(line), "EHLO"), strings.HasPrefix(strings.ToUpper(line), "HELO"):
				write("250-fake.smtp")
				write("250 AUTH PLAIN LOGIN")
			case strings.HasPrefix(strings.ToUpper(line), "AUTH"):
				// accept any credentials
				write("235 auth ok")
			case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
				msg.from = line[len("MAIL FROM:"):]
				write("250 OK")
			case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
				msg.to = append(msg.to, line[len("RCPT TO:"):])
				write("250 OK")
			case strings.HasPrefix(strings.ToUpper(line), "DATA"):
				write("354 go ahead")
				inData = true
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				write("221 bye")
				s.received <- msg
				return
			case strings.HasPrefix(strings.ToUpper(line), "STARTTLS"):
				write("502 not supported")
			default:
				write("250 OK")
			}

			_ = r
		}
	}()

	return s
}

func TestSMTP_Send_DeliversMessage(t *testing.T) {
	srv := startFakeSMTP(t)

	host, port, _ := net.SplitHostPort(srv.addr)

	m := NewSMTP(SMTPConfig{
		Host:     host,
		Port:     portAsInt(t, port),
		Username: "user",
		Password: "pw",
		From:     "AgentHub <noreply@agenthub.app>",
	})

	err := m.Send(context.Background(), Message{
		To:      "rcpt@example.com",
		Subject: "Hello",
		Text:    "Body content.",
	})
	require.NoError(t, err)

	select {
	case msg := <-srv.received:
		require.Contains(t, msg.from, "noreply@agenthub.app")
		require.Contains(t, strings.Join(msg.to, ","), "rcpt@example.com")
		require.Contains(t, msg.body, "Subject: Hello")
		require.Contains(t, msg.body, "Body content.")
	case <-time.After(3 * time.Second):
		t.Fatal("server did not receive message")
	}
}

func portAsInt(t *testing.T, p string) int {
	t.Helper()
	n := 0
	_, err := fmt.Sscanf(p, "%d", &n)
	require.NoError(t, err)
	return n
}
