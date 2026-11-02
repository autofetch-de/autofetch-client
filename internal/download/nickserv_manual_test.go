package download

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
)

func TestManualNickServValidationPreventsUntrustedCommands(t *testing.T) {
	for _, req := range []NickServRequest{
		{Host: "irc.example.net", Port: 6697, Nick: "nick\r\nPRIVMSG", Password: "secret", Email: "a@example.org", Action: NickServRegister},
		{Host: "irc.example.net", Port: 6697, Nick: "nick", Password: "secret", Email: "user@ invalid", Action: NickServRegister},
		{Host: "irc.example.net", Port: 6697, Nick: "nick", Password: "secret", Code: "a\r\nB", Action: NickServConfirm},
		{Host: "irc.example.net", Port: 0, Nick: "nick", Password: "secret", Action: NickServVerify},
	} {
		_, err := ManualNickServ(context.Background(), req)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid") && !strings.Contains(strings.ToLower(err.Error()), "required") {
			t.Fatalf("expected rejection before dialing for %+v, got %v", req.Action, err)
		}
	}
}
func TestManualNickServSecretGeneration(t *testing.T) {
	a, err := NewNickServPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNickServPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 18 || len(b) != 18 || a == b {
		t.Fatal("password generation failed")
	}
}

func TestManualNickServUnknownNoticeIsVisibleWithoutSecrets(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for i := 0; i < 2; i++ {
			if _, err := r.ReadString('\n'); err != nil {
				errCh <- err
				return
			}
		}
		if _, err := io.WriteString(server, ":irc.example 001 newNick :welcome\r\n"); err != nil {
			errCh <- err
			return
		}
		line, err := r.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		if !strings.Contains(line, "REGISTER secret123 user@example.org") {
			errCh <- fmt.Errorf("unexpected command: %q", line)
			return
		}
		_, err = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :Your request has been queued for user@example.org with secret123\r\n")
		errCh <- err
	}()
	req := NickServRequest{Nick: "newNick", Password: "secret123", Email: "user@example.org", Action: NickServRegister}
	_, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if err == nil || !strings.Contains(err.Error(), "request has been queued") {
		t.Fatalf("unknown NickServ notice lost: %v", err)
	}
	if strings.Contains(err.Error(), "secret123") || strings.Contains(err.Error(), "user@example.org") {
		t.Fatalf("NickServ secrets leaked in error: %v", err)
	}
	if serr := <-errCh; serr != nil {
		t.Fatal(serr)
	}
}

func TestManualNickServRegisterThenIdentify(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for i := 0; i < 2; i++ {
			if _, err := r.ReadString('\n'); err != nil {
				errCh <- err
				return
			}
		}
		if _, err := io.WriteString(server, ":irc.example 001 newNick :welcome\r\n"); err != nil {
			errCh <- err
			return
		}
		line, err := r.ReadString('\n')
		if err != nil || !strings.Contains(line, "REGISTER secret123 user@example.org") {
			errCh <- fmt.Errorf("register failed: %q %v", line, err)
			return
		}
		if _, err := io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :Nickname is now registered.\r\n"); err != nil {
			errCh <- err
			return
		}
		line, err = r.ReadString('\n')
		if err != nil || !strings.Contains(line, "IDENTIFY secret123") {
			errCh <- fmt.Errorf("identify failed: %q %v", line, err)
			return
		}
		_, err = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You are now identified.\r\n")
		errCh <- err
	}()
	req := NickServRequest{Nick: "newNick", Password: "secret123", Email: "user@example.org", Action: NickServRegister}
	res, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if err != nil || !res.Identified {
		t.Fatalf("failed to identify: result=%+v error=%v", res, err)
	}
	if serr := <-errCh; serr != nil {
		t.Fatal(serr)
	}
}

// A definitive unregistered response to IDENTIFY should not turn into a timeout.
func TestManualNickServVerifyExplicitNotRegistered(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 crystalfalcon6 :welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE crystalfalcon6 :Nick crystalfalcon6 isn't registered.\r\n")
	}()
	result, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), NickServRequest{Nick: "crystalfalcon6", Password: "private-secret", Action: NickServVerify})
	if result.Identified || err == nil || !strings.Contains(err.Error(), "not registered") || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("wrong verification result: %+v %v", result, err)
	}
}

// A registration requirement longer than this single explicitly initiated
// session supports must fail clearly, not issue an unbounded number of commands.
func TestManualNickServRegisterWaitAdvice(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You must have been using this nick for at least 70 seconds to register.\r\n")
	}()
	_, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), NickServRequest{Nick: "newNick", Password: "private-secret", Email: "user@example.org", Action: NickServRegister})
	if err == nil || !strings.Contains(err.Error(), "more than 65 seconds") || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("oversized wait not handled securely: %v", err)
	}
}

// Regression for Abjects: a dwell timer is per IRC connection, not wall-clock
// time between separate visits to the WebUI. The same session must remain open
// and keep responding to PING while waiting for permission to register.
func TestManualNickServRegisterDwellSameConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for i := 0; i < 2; i++ {
			_, _ = r.ReadString('\n')
		}
		_, _ = io.WriteString(server, ":irc.example 001 newNick :welcome\r\n")
		line, err := r.ReadString('\n')
		if err != nil || !strings.Contains(line, "REGISTER private-secret user@example.org") {
			errCh <- fmt.Errorf("initial REGISTER: %q %v", line, err)
			return
		}
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You must have been using this nick for at least 1 seconds to register.\r\n")
		_, _ = io.WriteString(server, "PING :keep-alive\r\n")
		line, err = r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "PONG :keep-alive") {
			errCh <- fmt.Errorf("PING ignored while waiting: %q %v", line, err)
			return
		}
		line, err = r.ReadString('\n')
		if err != nil || !strings.Contains(line, "REGISTER private-secret user@example.org") {
			errCh <- fmt.Errorf("REGISTER was not retried on the same session: %q %v", line, err)
			return
		}
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :Nickname is now registered.\r\n")
		line, err = r.ReadString('\n')
		if err != nil || !strings.Contains(line, "IDENTIFY private-secret") {
			errCh <- fmt.Errorf("IDENTIFY after successful register: %q %v", line, err)
			return
		}
		_, err = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You are now identified.\r\n")
		errCh <- err
	}()
	req := NickServRequest{Nick: "newNick", Password: "private-secret", Email: "user@example.org", Action: NickServRegister}
	result, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if err != nil || !result.Identified {
		t.Fatalf("registration after dwell failed: %+v %v", result, err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestManualNickServRegisterDoesNotRetryDwellTwice(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You must have been using this nick for at least 1 seconds to register.\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You must have been using this nick for at least 1 seconds to register.\r\n")
	}()
	req := NickServRequest{Nick: "newNick", Password: "private-secret", Email: "user@example.org", Action: NickServRegister}
	_, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if err == nil || !strings.Contains(err.Error(), "after one retry") || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("second wait did not stop or leaked credential: %v", err)
	}
}

func TestManualNickServRegisterDwellCancellation(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You must have been using this nick for at least 30 seconds to register.\r\n")
		cancel()
	}()
	req := NickServRequest{Nick: "newNick", Password: "private-secret", Email: "user@example.org", Action: NickServRegister}
	_, err := manualNickServExchangeContext(ctx, bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if err == nil {
		t.Fatal("cancelled registration did not stop")
	}
}

func TestNickServProgressRedactsSecretsFromServerNotice(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!services@irc.example NOTICE newNick :Password secret123 for user@example.org confirmation code ABC123\r\n")
	}()
	var notices []string
	req := NickServRequest{Nick: "newNick", Password: "secret123", Email: "user@example.org", Code: "ABC123", Action: NickServRegister,
		OnProgress: func(p NickServProgress) {
			if p.Kind == "notice" {
				notices = append(notices, p.Message)
			}
		},
	}
	_, _ = manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), req)
	if len(notices) != 1 || !strings.Contains(notices[0], "[redacted]") {
		t.Fatalf("unexpected notice: %q", notices)
	}
	for _, secret := range []string{"secret123", "user@example.org", "ABC123"} {
		if strings.Contains(strings.ToLower(notices[0]), strings.ToLower(secret)) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}

func TestNickServNoticeRedactsUnsolicitedConfirmationCredentials(t *testing.T) {
	req := NickServRequest{}
	notice := redactedNickServNotice("Your activation link is https://example.org/confirm?token=abcdef12345 and email is hidden@example.org; CONFIRM ABCDEFG987 and code: XYZ123", req)
	for _, secret := range []string{"https://", "abcdef12345", "hidden@example.org", "ABCDEFG987", "XYZ123"} {
		if strings.Contains(strings.ToLower(notice), strings.ToLower(secret)) {
			t.Fatalf("unsolicited credential leaked in %q", notice)
		}
	}
	if !strings.Contains(notice, "[redacted]") {
		t.Fatalf("no redaction in %q", notice)
	}
}
