package desktop

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	"github.com/autofetch-de/autofetch-client/internal/nickservflow"
)

// openNickServDialog uses the same audited IRC exchange as the WebUI. It does
// not register on startup or while downloading; every operation is explicit.
func (v *mainView) openNickServDialog(w fyne.Window, svc *app.Service, host, nick string) {
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	email := widget.NewEntry()
	email.SetPlaceHolder(v.l.T("irc.desktop.email_placeholder"))
	code := widget.NewEntry()
	code.SetPlaceHolder(v.l.T("irc.desktop.code_placeholder"))
	ack := widget.NewCheck(v.l.T("irc.desktop.ack_short"), nil)
	ackHint := widget.NewLabel(v.l.T("irc.desktop.acknowledge"))
	ackHint.Wrapping = fyne.TextWrapWord
	// A disabled Entry renders its text in Fyne's muted disabled color.
	// NickServ replies are read-only information, not disabled form input.
	progress := widget.NewLabel(v.l.T("irc.desktop.progress_empty"))
	progress.Wrapping = fyne.TextWrapWord
	progress.Selectable = true // Allow copying redacted NickServ notices for diagnostics.
	activityScroll := container.NewVScroll(progress)
	activityScroll.SetMinSize(fyne.NewSize(0, 132))
	register := widget.NewButton(v.l.T("irc.desktop.register"), nil)
	retry := widget.NewButton(v.l.T("irc.desktop.retry"), nil)
	verify := widget.NewButton(v.l.T("irc.desktop.verify"), nil)
	reset := widget.NewButton(v.l.T("irc.desktop.reset"), nil)
	// If a pending registration has lost its password, reset is the only
	// actionable recovery step. Keep it accessible without scrolling.
	resetPrimary := widget.NewButton(v.l.T("irc.desktop.reset"), nil)
	skip := widget.NewButton(v.l.T("irc.desktop.skip"), nil)
	buttons := []*widget.Button{register, retry, verify, reset, resetPrimary, skip}
	busy := false

	readNetwork := func() (config.IRCNetwork, bool) {
		cfg := svc.IRCConfig().WithDefaults()
		for _, n := range cfg.Networks {
			if strings.EqualFold(n.Host, host) && strings.EqualFold(n.Nick, nick) {
				return n, true
			}
		}
		return config.IRCNetwork{}, false
	}
	// The email is tied to the selected network, not to global IRC settings.
	if n, exists := readNetwork(); exists {
		email.SetText(n.RegistrationEmail)
	}
	refresh := func() {
		// Only the action matching the current registration state is prominent.
		// Keep it in the dialog footer so scrolling never hides it.
		register.Hide()
		verify.Hide()
		resetPrimary.Hide()
		n, exists := readNetwork()
		if !exists {
			status.SetText(v.l.T("irc.desktop.identity_changed"))
			for _, b := range buttons {
				b.Disable()
			}
			return
		}
		if n.NickServ.RegistrationPending {
			if n.NickServ.Password == "" {
				status.SetText(v.l.T("irc.desktop.pending_no_password"))
			} else {
				status.SetText(v.l.T("irc.desktop.status_pending"))
			}
		} else if n.NickServ.Enabled && n.NickServ.Password != "" {
			status.SetText(v.l.T("irc.desktop.status_identified"))
		} else {
			status.SetText(v.l.T("irc.desktop.status_not_registered"))
		}
		if n.NickServ.RegistrationPending && n.NickServ.Password != "" {
			verify.Show()
		} else if n.NickServ.RegistrationPending {
			resetPrimary.Show()
		} else if n.NickServ.Password == "" && !n.NickServ.Enabled {
			register.Show()
		}
		if busy {
			for _, b := range buttons {
				b.Disable()
			}
			return
		}
		for _, b := range buttons {
			b.Disable()
		}
		if n.NickServ.RegistrationPending {
			reset.Enable()
			if n.NickServ.Password == "" {
				resetPrimary.Enable()
			}
			if n.NickServ.Password != "" {
				retry.Enable()
				verify.Enable()
			}
		} else if n.NickServ.Password == "" && !n.NickServ.Enabled {
			register.Enable()
			if n.NickServ.RequiresRegistration {
				skip.Enable()
			}
		}
	}
	lines := make([]string, 0, 100)
	appendProgress := func(line string) {
		if strings.TrimSpace(line) == "" {
			return
		}
		if len(lines) >= 100 {
			lines = lines[1:]
		}
		lines = append(lines, line)
		progress.SetText(strings.Join(lines, "\n"))
		activityScroll.ScrollToBottom()
	}
	execute := func(action nickservflow.Action) {
		if busy {
			return
		}
		busy = true
		refresh()
		lines = nil
		progress.SetText("")
		req := nickservflow.Request{
			Host: host, Nick: nick, Action: action,
			Email: strings.TrimSpace(email.Text), Code: strings.TrimSpace(code.Text),
			RulesAcknowledged: ack.Checked,
			OnProgress: func(p download.NickServProgress) {
				// Only sanitized notices are emitted by ManualNickServ.
				msg := p.Kind
				if p.Message != "" {
					msg += ": " + p.Message
				}
				if p.WaitSeconds > 0 {
					msg += fmt.Sprintf(" (%ds)", p.WaitSeconds)
				}
				fyne.Do(func() { appendProgress(msg) })
			},
		}
		go func() {
			result, err := nickservflow.Run(context.Background(), svc, req, nil)
			fyne.Do(func() {
				busy = false
				if err != nil {
					appendProgress(v.l.T("irc.desktop.failed") + ": " + err.Error())
				} else if result.Identified {
					appendProgress(v.l.T("irc.desktop.success"))
				} else if result.Pending {
					appendProgress(v.l.T("irc.desktop.pending_next"))
				} else if result.Reset || result.Unregistered {
					appendProgress(v.l.T("irc.desktop.saved"))
				}
				refresh()
			})
		}()
	}
	register.OnTapped = func() { execute(nickservflow.Register) }
	retry.OnTapped = func() { execute(nickservflow.Retry) }
	verify.OnTapped = func() { execute(nickservflow.Verify) }
	confirmReset := func() {
		dialog.ShowConfirm(v.l.T("irc.desktop.reset"), v.l.T("irc.desktop.reset_warning"), func(ok bool) {
			if ok {
				execute(nickservflow.Reset)
			}
		}, w)
	}
	reset.OnTapped = confirmReset
	resetPrimary.OnTapped = confirmReset
	skip.OnTapped = func() { execute(nickservflow.ProceedUnregistered) }
	refresh()
	explanation := widget.NewLabel(v.l.T("irc.desktop.explanation"))
	explanation.Wrapping = fyne.TextWrapWord
	// Keep only the network identity pinned; the detailed status scrolls
	// with the rest of the registration form to preserve vertical space.
	header := widget.NewLabel(host + " · " + nick)
	header.TextStyle = fyne.TextStyle{Bold: true}
	troubleshooting := widget.NewAccordionItem(v.l.T("irc.desktop.troubleshooting"), container.NewVBox(
		container.NewHBox(retry), container.NewHBox(reset), container.NewHBox(skip),
	))
	body := container.NewVBox(
		status,
		explanation,
		widget.NewLabel(v.l.T("irc.desktop.email_title")), email,
		ack, ackHint,
		widget.NewLabel(v.l.T("irc.desktop.confirm_title")), code,
		widget.NewAccordion(troubleshooting),
		widget.NewSeparator(), widget.NewLabel(v.l.T("irc.desktop.activity")), activityScroll,
	)
	// Main actions are outside the scroll area and visible even in a short window.
	actions := container.NewVBox(widget.NewSeparator(), register, verify, resetPrimary)
	content := container.NewBorder(header, actions, nil, nil, container.NewVScroll(body))
	d := dialog.NewCustom(v.l.T("irc.desktop.title"), v.l.T("action.close"), content, w)
	d.Resize(fyne.NewSize(650, 580))
	d.Show()
}
