package download

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRegisteredNickJoinErrorIsActionable(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		line, err := r.ReadString('\n')
		if err != nil {
			peerDone <- err
			return
		}
		if line != "JOIN #mg-chat\r\n" {
			peerDone <- errors.New("unexpected JOIN: " + line)
			return
		}
		_, err = io.WriteString(peer, ":stormy.fr.eu.abjects.net 477 rapidPanda79 #mg-chat :You need a registered nick to join that channel.\r\n")
		peerDone <- err
	}()
	rw := bufio.NewReadWriter(bufio.NewReader(local), bufio.NewWriter(local))
	err := joinRequiredChannels(ctx, local, rw, func(s string) error { return writeIRCLine(rw, s) }, "rapidPanda79", []string{"#mg-chat", "#moviegods"}, time.Second, "bot", nil)
	if !errors.Is(err, ErrIRCRegisteredNickRequired) || !strings.Contains(err.Error(), "channel=#mg-chat") {
		t.Fatalf("expected registration-required error with rejected prerequisite channel, got %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

func TestNickServRegistrationOnJoinRetriesOnlyOnce(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		want := func(expected string) error {
			line, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			if line != expected+"\r\n" {
				return errors.New("unexpected IRC line: " + line + ", expected " + expected)
			}
			return nil
		}
		if err := want("JOIN #mg-chat"); err != nil {
			done <- err
			return
		}
		if _, err := io.WriteString(peer, ":server 477 nick #mg-chat :You need a registered nick to join that channel.\r\n"); err != nil {
			done <- err
			return
		}
		if err := want("PRIVMSG NickServ :REGISTER securepassword user@example.org"); err != nil {
			done <- err
			return
		}
		if _, err := io.WriteString(peer, ":NickServ NOTICE nick :Nickname is now registered\r\n"); err != nil {
			done <- err
			return
		}
		if err := want("PRIVMSG NickServ :IDENTIFY securepassword"); err != nil {
			done <- err
			return
		}
		if _, err := io.WriteString(peer, ":NickServ NOTICE nick :You are now identified\r\n"); err != nil {
			done <- err
			return
		}
		if err := want("JOIN #mg-chat"); err != nil {
			done <- err
			return
		}
		if _, err := io.WriteString(peer, ":nick!u@h JOIN :#mg-chat\r\n"); err != nil {
			done <- err
			return
		}
		if err := want("JOIN #moviegods"); err != nil {
			done <- err
			return
		}
		_, err := io.WriteString(peer, ":nick!u@h JOIN :#moviegods\r\n")
		done <- err
	}()
	rw := bufio.NewReadWriter(bufio.NewReader(local), bufio.NewWriter(local))
	attempts := 0
	joinErr := joinRequiredChannels(ctx, local, rw, func(s string) error { return writeIRCLine(rw, s) }, "nick", []string{"#mg-chat", "#moviegods", "#mg-chat"}, time.Second, "bot", func() error {
		attempts++
		return performNickServHandshake(ctx, local, rw, "irc.test.example", "user@example.org", "securepassword", nil)
	})
	if joinErr != nil {
		t.Fatal(joinErr)
	}
	if attempts != 1 {
		t.Fatalf("expected one registration attempt, got %d", attempts)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAutoRegistrationMustNotRunForAbjects(t *testing.T) {
	opt := XDCCOptions{AutoRegister: true, RegistrationEmail: "user@example.org"}
	for _, host := range []string{"irc.abjects.net", "ABJECTS.NET", "irc.abjects.net."} {
		if autoRegistrationAllowed(opt, host) {
			t.Fatalf("registration must be manual for %s", host)
		}
	}
	if autoRegistrationAllowed(opt, "irc.example.net") {
		t.Fatal("account creation must be a manual WebUI action on all networks")
	}
	opt.NickServPassword = "existing"
	if autoRegistrationAllowed(opt, "irc.example.net") {
		t.Fatal("existing credentials must be identified, not replaced by a new registration")
	}
}

func TestGeneratedNickServPasswordsAreRandom(t *testing.T) {
	first, err := generateRegistrationPassword()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateRegistrationPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 18 || len(second) != 18 || first == second {
		t.Fatalf("unexpected generated password properties")
	}
}
