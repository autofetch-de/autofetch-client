package download

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/nickservstate"
)

func TestNickServSourceRejectsServerTextAndForgedNumerics(t *testing.T) {
	cases := []struct {
		line string
		ok   bool
	}{
		{":irc.scene.example NOTICE newNick :NickServ requires an email to register\r\n", false},
		{":irc.scene.example 001 newNick :Welcome to NickServ services\r\n", false},
		{":irc.scene.example 433 * newNick :Nickname is registered with NickServ\r\n", false},
		{":services.scene.example NOTICE newNick :registration successful\r\n", false},
		{":NickServ!services@irc.scene.example NOTICE newNick :Registration successful\r\n", true},
		{":services!services@irc.scene.example NOTICE newNick :Registration successful\r\n", true},
		{":atheme!service@irc.scene.example PRIVMSG newNick :Registration successful\r\n", true},
		{":irc.scene.example PRIVMSG newNick :NickServ says registration successful\r\n", false},
	}
	for _, tc := range cases {
		if got := isNickServSource(tc.line); got != tc.ok {
			t.Errorf("isNickServSource(%q)=%t, want %t", tc.line, got, tc.ok)
		}
	}
}

func TestNickServConfirmationRequiresAffirmativeNotice(t *testing.T) {
	for _, body := range []string{
		"For help with email registration, type /msg NickServ HELP REGISTER",
		"Use a valid email address when you register.",
		"You can verify your email in your account settings.",
		"No existing pending credentials; registration failed.",
	} {
		line := ":NickServ!service@host NOTICE newNick :" + body + "\r\n"
		if isNickServConfirmationPending(line) {
			t.Errorf("generic or negative notice was treated as accepted: %q", body)
		}
	}
	for _, body := range []string{
		"A confirmation email has been sent to your address.",
		"Please check your email for your activation instructions.",
		"Your account is awaiting confirmation.",
		"A confirmation code has been sent.",
	} {
		line := ":NickServ!service@host NOTICE newNick :" + body + "\r\n"
		if !isNickServConfirmationPending(line) {
			t.Errorf("explicit confirmation request not recognized: %q", body)
		}
	}
}

// Script one real IRC session over net.Pipe, so the client's command order,
// response classification, and connection lifetime are all exercised.
func scriptedNickServSession(t *testing.T, action NickServAction, welcome string, serverReplies []string, expectIdentify bool) (NickServResult, error) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for i := 0; i < 2; i++ {
			if _, err := r.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
		if _, err := io.WriteString(server, welcome); err != nil {
			done <- err
			return
		}
		if !ircWelcomeHasNick(welcome, "newNick") {
			done <- nil
			return
		}
		command, err := r.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if (action == NickServRegister && !strings.Contains(command, "REGISTER private-secret person@example.org")) ||
			(action == NickServVerify && !strings.Contains(command, "IDENTIFY private-secret")) ||
			(action == NickServConfirm && !strings.Contains(command, "CONFIRM abcdef")) {
			done <- fmt.Errorf("wrong outgoing NickServ command: %q", command)
			return
		}
		for _, reply := range serverReplies {
			if _, err = io.WriteString(server, reply); err != nil {
				done <- err
				return
			}
		}
		if expectIdentify {
			command, err = r.ReadString('\n')
			if err != nil || !strings.Contains(command, "IDENTIFY private-secret") {
				done <- fmt.Errorf("missing IDENTIFY after REGISTER: %q: %v", command, err)
				return
			}
		}
		done <- nil
	}()
	res, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), NickServRequest{
		Nick: "newNick", Password: "private-secret", Email: "person@example.org", Code: "abcdef", Action: action,
	})
	if scriptErr := <-done; scriptErr != nil {
		t.Fatal(scriptErr)
	}
	return res, err
}

func TestManualNickServMisleadingServerMessageCannotCompleteRegistration(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 newNick :Welcome to NickServ services\r\n", []string{
		":irc.example NOTICE newNick :NickServ emailed you a confirmation code.\r\n",
		":NickServ!service@irc.example NOTICE newNick :Use a valid email address when registering.\r\n",
	}, false)
	if res.Pending || res.Identified || res.Status != nickservstate.Unconfirmed || err == nil || !strings.Contains(err.Error(), "outcome unconfirmed") {
		t.Fatalf("false pending registration: %+v %v", res, err)
	}
}

func TestManualNickServHelpThenDefinitiveRejection(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 newNick :Welcome\r\n", []string{
		":NickServ!service@irc.example NOTICE newNick :Please enter a valid email address to register.\r\n",
		":NickServ!service@irc.example NOTICE newNick :Registration failed: invalid email.\r\n",
	}, false)
	if err == nil || res.Pending || res.Status != nickservstate.Failed {
		t.Fatalf("definitive rejection misclassified: %+v %v", res, err)
	}
}

func TestManualNickServEmailConfirmationAcknowledged(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 newNick :Welcome\r\n", []string{
		":NickServ!service@irc.example NOTICE newNick :A confirmation email has been sent to person@example.org.\r\n",
	}, false)
	if err != nil || !res.Pending || res.Identified || res.Status != nickservstate.AwaitingConfirmation {
		t.Fatalf("explicit email confirmation not tracked: %+v %v", res, err)
	}
}

func TestManualNickServRegisteredIsNotAutomaticallyIdentified(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 newNick :Welcome\r\n", []string{
		":NickServ!service@irc.example NOTICE newNick :Nickname is now registered.\r\n",
	}, true)
	if err != nil || !res.Pending || res.Identified || res.Status != nickservstate.RegisteredUnverified {
		t.Fatalf("registration acknowledgement was mistaken for login: %+v %v", res, err)
	}
}

func TestManualNickServServerAssignsWrongNick(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 collision_ :Welcome\r\n", nil, false)
	if err == nil || res.Status != nickservstate.Failed || !strings.Contains(err.Error(), "different nickname") {
		t.Fatalf("server-chosen alternative nick accepted: %+v %v", res, err)
	}
}

func TestManualNickServNoticeForOtherNickIgnored(t *testing.T) {
	res, err := scriptedNickServSession(t, NickServRegister, ":irc.example 001 newNick :Welcome\r\n", []string{
		":NickServ!service@irc.example NOTICE someoneElse :You are now identified.\r\n",
	}, false)
	if err == nil || res.Pending || res.Identified || res.Status != nickservstate.Unconfirmed {
		t.Fatalf("third-party identity completed registration: %+v %v", res, err)
	}
}

func TestManualNickServAlreadyRegisteredAttemptsExistingPassword(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :Welcome\r\n")
		initial, err := r.ReadString('\n')
		if err != nil || !strings.Contains(initial, "REGISTER private-secret person@example.org") {
			done <- fmt.Errorf("wrong registration: %q %v", initial, err)
			return
		}
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :Nickname is already registered.\r\n")
		login, err := r.ReadString('\n')
		if err != nil || !strings.Contains(login, "IDENTIFY private-secret") {
			done <- fmt.Errorf("existing credentials were not verified: %q %v", login, err)
			return
		}
		_, err = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You are now identified.\r\n")
		done <- err
	}()
	res, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), NickServRequest{
		Nick: "newNick", Password: "private-secret", Email: "person@example.org", Action: NickServRegister,
	})
	if scriptErr := <-done; scriptErr != nil {
		t.Fatal(scriptErr)
	}
	if err != nil || !res.Identified || res.Status != nickservstate.Identified {
		t.Fatalf("saved credentials cannot recover an already registered nick: %+v %v", res, err)
	}
}

func TestManualNickServConfirmedButNotIdentifiedRemainsRegisteredUnverified(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":irc.example 001 newNick :Welcome\r\n")
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :Nickname is now registered.\r\n")
		_, _ = r.ReadString('\n')
		_, err := io.WriteString(server, ":NickServ!service@irc.example NOTICE newNick :You are not identified.\r\n")
		done <- err
	}()
	res, err := manualNickServExchange(bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), NickServRequest{
		Nick: "newNick", Password: "private-secret", Email: "person@example.org", Action: NickServRegister,
	})
	if scriptErr := <-done; scriptErr != nil {
		t.Fatal(scriptErr)
	}
	if err == nil || !res.Pending || res.Identified || res.Status != nickservstate.RegisteredUnverified {
		t.Fatalf("identify failure erased registration evidence: %+v %v", res, err)
	}
}

func TestNickServConfirmSuccessRequiresPositiveAcknowledgement(t *testing.T) {
	for _, body := range []string{
		"Your email address of person@example.org has been confirmed",
		"Your nickname has been confirmed",
		"Confirmation successful",
		"Account activated",
	} {
		if !isNickServConfirmSuccess(":NickServ NOTICE newNick :" + body + "\r\n") {
			t.Errorf("confirmed identity was not recognized: %q", body)
		}
	}
	for _, body := range []string{
		"Your email address has not been confirmed",
		"Confirmation failed; invalid code",
		"You can confirm your nickname later",
	} {
		if isNickServConfirmSuccess(":NickServ NOTICE newNick :" + body + "\r\n") {
			t.Errorf("unconfirmed identity incorrectly accepted: %q", body)
		}
	}
}
