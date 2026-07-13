package desktop

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/config"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	desktopdriver "fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"

	"fyne.io/fyne/v2/widget"
	"github.com/autofetch-de/autofetch-client/internal/desktop/assets"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

func Run(ctx context.Context, svc *app.Service) error {
	a := fyneapp.NewWithID("de.autofetch.client")
	a.SetIcon(assets.AppIconPNG)
	w := a.NewWindow("autofetch-client")
	w.Resize(fyneSize())

	view := newMainView(w, svc)
	w.SetContent(view.root)

	quitting := false
	w.SetCloseIntercept(func() {
		if quitting {
			return
		}
		w.Hide()
		view.setActionText("Fenster wurde in den Tray minimiert.")
	})

	if desk, ok := a.(desktopdriver.App); ok {
		openItem := fyne.NewMenuItem("Fenster öffnen", func() {
			w.Show()
			w.RequestFocus()
		})
		startItem := fyne.NewMenuItem("Start", func() {
			view.startOrResume(svc)
		})
		pauseItem := fyne.NewMenuItem("Pause", func() {
			view.stopOrPause(svc)
		})
		settingsItem := fyne.NewMenuItem("Einstellungen", func() {
			view.openSettingsDialog(w, svc)
		})
		repairPairingItem := fyne.NewMenuItem("Neu koppeln", func() {
			view.confirmRePair(w, svc)
		})
		quitItem := fyne.NewMenuItem("Beenden", func() {
			quitting = true
			_ = svc.Stop()
			w.Close()
			a.Quit()
		})

		menu := fyne.NewMenu("autofetch",
			openItem,
			fyne.NewMenuItemSeparator(),
			startItem,
			pauseItem,
			settingsItem,
			repairPairingItem,
			fyne.NewMenuItemSeparator(),
			quitItem,
		)
		view.trayApp = desk
		desk.SetSystemTrayIcon(assets.TrayIconPending)
		desk.SetSystemTrayMenu(menu)
	}

	if err := svc.Start(); err != nil {
		view.setActionText(err.Error())
	}

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fyne.Do(func() {
					view.refresh(svc)
				})
			}
		}
	}()

	w.ShowAndRun()
	_ = svc.Stop()
	return nil
}

type uiState int

const (
	uiStatePairing uiState = iota
	uiStateIdle
	uiStateDownloading
)

type mainView struct {
	root fyne.CanvasObject

	trayApp      desktopdriver.App
	lastTrayIcon string

	titleLabel *widget.Label
	statusBar  *widget.Label

	primaryAction       *widget.Button
	secondaryAction     *widget.Button
	settingsAction      *widget.Button
	repairPairingAction *widget.Button

	contentHolder *fyne.Container

	pairCode    *widget.RichText
	pairExpiry  *widget.Label
	pairingCard *widget.Card
	idleMessage *widget.Label

	logEntry     *widget.Entry
	logAccordion *widget.Accordion

	progressBar *widget.ProgressBar
	fileLabel   *widget.Label

	overrideStatus string
	lastState      uiState
	lastLogsKey    string
}

func newMainView(w fyne.Window, svc *app.Service) *mainView {
	v := &mainView{lastState: uiState(-1)}
	v.titleLabel = widget.NewLabelWithStyle("autofetch-client", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.statusBar = widget.NewLabel("")
	v.statusBar.Wrapping = fyne.TextTruncate
	v.contentHolder = container.NewMax()

	v.primaryAction = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() {
		v.startOrResume(svc)
	})
	v.secondaryAction = widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), func() {
		v.stopOrPause(svc)
	})
	v.settingsAction = widget.NewButtonWithIcon("Einstellungen", theme.SettingsIcon(), func() {
		v.openSettingsDialog(w, svc)
	})
	v.repairPairingAction = widget.NewButtonWithIcon("Neu koppeln", theme.ViewRefreshIcon(), func() {
		v.confirmRePair(w, svc)
	})
	for _, btn := range []*widget.Button{v.primaryAction, v.secondaryAction, v.settingsAction, v.repairPairingAction} {
		btn.Importance = widget.LowImportance
	}

	v.pairCode = widget.NewRichTextFromMarkdown("**`—`**")
	v.pairCode.Wrapping = fyne.TextWrapOff
	v.pairExpiry = widget.NewLabel("—")
	v.pairExpiry.Alignment = fyne.TextAlignCenter
	v.pairExpiry.Wrapping = fyne.TextWrapWord
	v.idleMessage = widget.NewLabelWithStyle("Nichts zu tun - warte auf Aufträge.", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	v.idleMessage.Wrapping = fyne.TextWrapWord

	copyBtn := widget.NewButtonWithIcon("Code kopieren", theme.ContentCopyIcon(), func() {
		snap := svc.Snapshot()
		code := strings.TrimSpace(snap.PairingCode)
		if code == "" {
			v.setActionText("Aktuell ist kein Kopplungscode verfügbar.")
			return
		}
		w.Clipboard().SetContent(code)
		v.setActionText("Pairing-Code kopiert.")
	})
	openBtn := widget.NewButtonWithIcon("Pairing-Seite öffnen", theme.ComputerIcon(), func() {
		snap := svc.Snapshot()
		target := buildPairingURL(snap.PairingURL, snap.PairingCode)
		if err := fyne.CurrentApp().OpenURL(mustURL(target)); err != nil {
			v.setActionText(err.Error())
			return
		}
		v.setActionText("Pairing-Seite geöffnet.")
	})
	pairButtons := container.NewGridWithColumns(2, copyBtn, openBtn)
	pairContent := container.NewVBox(
		widget.NewLabel("Diesen Code auf der Pairing-Seite eingeben:"),
		container.NewPadded(container.NewCenter(container.NewHBox(v.pairCode))),
		v.pairExpiry,
		pairButtons,
	)
	v.pairingCard = widget.NewCard("Client koppeln", "", pairContent)

	v.logEntry = widget.NewMultiLineEntry()
	v.logEntry.Wrapping = fyne.TextWrapWord
	v.logEntry.SetMinRowsVisible(12)
	logScroll := container.NewVScroll(v.logEntry)
	logBg := canvas.NewRectangle(theme.InputBackgroundColor())
	v.logAccordion = widget.NewAccordion(widget.NewAccordionItem("Log anzeigen", container.NewStack(logBg, container.NewPadded(logScroll))))

	v.progressBar = widget.NewProgressBar()
	v.progressBar.Min = 0
	v.progressBar.Max = 1
	v.fileLabel = widget.NewLabel("")
	v.fileLabel.Alignment = fyne.TextAlignCenter
	v.fileLabel.Wrapping = fyne.TextWrapWord

	headerRow := container.NewHBox(
		v.titleLabel,
		layout.NewSpacer(),
		v.primaryAction,
		v.secondaryAction,
		v.settingsAction,
		v.repairPairingAction,
	)
	top := container.NewPadded(container.NewVBox(
		headerRow,
		widget.NewSeparator(),
	))
	statusWrap := container.NewPadded(v.statusBar)
	statusBg := canvas.NewRectangle(theme.InputBackgroundColor())
	statusArea := container.NewStack(statusBg, statusWrap)

	content := container.NewPadded(v.contentHolder)
	v.root = container.NewBorder(top, statusArea, nil, nil, content)
	v.refresh(svc)
	return v
}

func (v *mainView) refresh(svc *app.Service) {
	snap := svc.Snapshot()
	v.refreshTitle(snap)
	v.refreshLogs(snap)
	v.refreshActions(snap)
	state := deriveUIState(snap)
	if state != v.lastState {
		switch state {
		case uiStatePairing:
			v.setMainContent(v.buildPairingView(snap))
		case uiStateDownloading:
			v.setMainContent(v.buildDownloadingView(snap))
		default:
			v.setMainContent(v.buildIdleView(snap))
		}
		v.lastState = state
	}
	if state == uiStatePairing {
		v.refreshPairingView(snap)
	}
	if state == uiStateDownloading {
		v.refreshDownloadView(snap)
	}

	if strings.TrimSpace(v.overrideStatus) != "" {
		v.statusBar.SetText(v.overrideStatus)
		v.refreshTray(snap)
		return
	}
	v.statusBar.SetText(buildStatusLine(snap))
	v.refreshTray(snap)
}

func (v *mainView) refreshTray(snap observe.Snapshot) {
	if v.trayApp == nil {
		return
	}
	iconKey, iconRes := trayIconForSnapshot(snap)
	if iconKey == v.lastTrayIcon {
		return
	}
	v.trayApp.SetSystemTrayIcon(iconRes)
	v.lastTrayIcon = iconKey
}

func trayIconForSnapshot(snap observe.Snapshot) (string, fyne.Resource) {
	if !snap.Paired {
		return "pending", assets.TrayIconPending
	}
	if snap.ActiveDownload != nil {
		return "okay", assets.TrayIconOkay
	}
	if snap.Connected {
		return "okay", assets.TrayIconOkay
	}
	if !snap.Running {
		return "pending", assets.TrayIconPending
	}
	if strings.TrimSpace(snap.LastError) != "" {
		return "fail", assets.TrayIconFail
	}
	return "pending", assets.TrayIconPending
}

func (v *mainView) refreshTitle(snap observe.Snapshot) {
	title := strings.TrimSpace(snap.ClientName)
	if title == "" {
		title = "autofetch-client"
	}
	v.titleLabel.SetText(title)
}

func (v *mainView) refreshActions(snap observe.Snapshot) {
	v.settingsAction.Enable()
	if snap.Paired {
		v.primaryAction.Enable()
		v.repairPairingAction.Enable()
	} else {
		v.primaryAction.Disable()
		v.secondaryAction.Disable()
		v.repairPairingAction.Disable()
		v.primaryAction.SetText("Start")
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.SetText("Pause")
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
		return
	}

	switch {
	case snap.ActiveDownload != nil:
		v.primaryAction.Disable()
		v.primaryAction.SetText("Start")
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Enable()
		v.secondaryAction.SetText("Pause")
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
	case snap.Running:
		v.primaryAction.Disable()
		v.primaryAction.SetText("Start")
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Enable()
		v.secondaryAction.SetText("Pause")
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
	default:
		label := "Start"
		if strings.TrimSpace(snap.LastError) != "" {
			label = "Resume"
		}
		v.primaryAction.Enable()
		v.primaryAction.SetText(label)
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Disable()
		v.secondaryAction.SetText("Pause")
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
	}
}

func (v *mainView) refreshLogs(snap observe.Snapshot) {
	joined := strings.Join(snap.Logs, "\n")
	if joined == v.lastLogsKey {
		return
	}
	v.lastLogsKey = joined
	v.logEntry.SetText(joined)
}

func (v *mainView) setMainContent(obj fyne.CanvasObject) {
	v.contentHolder.Objects = []fyne.CanvasObject{obj}
	v.contentHolder.Refresh()
}

func (v *mainView) buildPairingView(_ observe.Snapshot) fyne.CanvasObject {
	return container.NewVBox(
		widget.NewSeparator(),
		v.pairingCard,
		layout.NewSpacer(),
	)
}

func (v *mainView) refreshPairingView(snap observe.Snapshot) {
	pairCode := strings.TrimSpace(snap.PairingCode)
	if pairCode == "" {
		pairCode = "—"
	}
	v.pairCode.ParseMarkdown("**`" + pairCode + "`**")

	expires := formatTimestamp(snap.PairingExpiry)
	remaining := formatRemainingTimestamp(snap.PairingExpiry)
	switch {
	case expires != "" && remaining != "":
		v.pairExpiry.SetText(fmt.Sprintf("Gültig bis %s (%s)", expires, remaining))
	case expires != "":
		v.pairExpiry.SetText("Gültig bis " + expires)
	default:
		v.pairExpiry.SetText("Gültigkeit wird ermittelt …")
	}
}

func (v *mainView) buildIdleView(_ observe.Snapshot) fyne.CanvasObject {
	return container.NewBorder(
		container.NewVBox(
			layout.NewSpacer(),
			v.idleMessage,
			widget.NewLabel(""),
		),
		nil,
		nil,
		nil,
		v.logAccordion,
	)
}

func (v *mainView) buildDownloadingView(snap observe.Snapshot) fyne.CanvasObject {
	v.refreshDownloadView(snap)
	return container.NewVBox(
		layout.NewSpacer(),
		v.progressBar,
		widget.NewLabel(""),
		v.fileLabel,
		layout.NewSpacer(),
	)
}

func (v *mainView) refreshDownloadView(snap observe.Snapshot) {
	d := snap.ActiveDownload
	if d == nil {
		v.progressBar.SetValue(0)
		v.fileLabel.SetText("")
		return
	}

	progress := 0.0
	if d.Total > 0 {
		progress = float64(d.Downloaded) / float64(d.Total)
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	v.progressBar.SetValue(progress)
	v.fileLabel.SetText(strings.TrimSpace(d.Target))
}

func (v *mainView) startOrResume(svc *app.Service) {
	if err := svc.Start(); err != nil {
		v.setActionText(err.Error())
		return
	}
	v.setActionText("Client wurde gestartet.")
	v.refresh(svc)
}

func (v *mainView) stopOrPause(svc *app.Service) {
	if err := svc.Stop(); err != nil {
		v.setActionText(err.Error())
		return
	}
	v.setActionText("Client wurde pausiert.")
	v.refresh(svc)
}

func sanitizeIRCNetworksForEditor(networks []config.IRCNetwork) []config.IRCNetwork {
	out := make([]config.IRCNetwork, len(networks))
	for i, n := range networks {
		out[i] = n
		out[i].NickServ.Password = ""
		out[i].SASL.Username = ""
		out[i].SASL.Password = ""
	}
	return out
}

func preserveExistingIRCSecrets(next, existing []config.IRCNetwork) []config.IRCNetwork {
	byHost := map[string]config.IRCNetwork{}
	for _, n := range existing {
		host := strings.TrimSpace(strings.ToLower(n.Host))
		if host == "" {
			continue
		}
		byHost[host] = n
	}
	for i := range next {
		host := strings.TrimSpace(strings.ToLower(next[i].Host))
		prev, ok := byHost[host]
		if !ok {
			continue
		}
		if strings.TrimSpace(next[i].NickServ.Password) == "" {
			next[i].NickServ.Password = prev.NickServ.Password
		}
		if strings.TrimSpace(next[i].SASL.Username) == "" {
			next[i].SASL.Username = prev.SASL.Username
		}
		if strings.TrimSpace(next[i].SASL.Password) == "" {
			next[i].SASL.Password = prev.SASL.Password
		}
	}
	return next
}

func buildIRCSecretStatus(networks []config.IRCNetwork) string {
	lines := make([]string, 0, len(networks))
	for _, n := range networks {
		host := strings.TrimSpace(n.Host)
		if host == "" {
			host = strings.TrimSpace(n.Name)
		}
		if host == "" {
			continue
		}
		status := make([]string, 0, 2)
		if strings.TrimSpace(n.NickServ.Password) != "" {
			status = append(status, "NickServ-Passwort gespeichert")
		}
		if strings.TrimSpace(n.SASL.Username) != "" || strings.TrimSpace(n.SASL.Password) != "" {
			status = append(status, "SASL-Zugang gespeichert")
		}
		if len(status) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", host, strings.Join(status, ", ")))
	}
	if len(lines) == 0 {
		return "Keine lokalen IRC-Zugangsdaten gespeichert. Passwortfelder bleiben im JSON-Editor absichtlich leer; leere Felder ändern bestehende Secrets nicht."
	}
	return "Lokal gespeicherte IRC-Zugangsdaten\n- " + strings.Join(lines, "\n- ") + "\n\nPasswortfelder bleiben im JSON-Editor absichtlich leer; leere Felder ändern bestehende Secrets nicht."
}

func (v *mainView) openSettingsDialog(w fyne.Window, svc *app.Service) {
	snap := svc.Snapshot()
	downloadDir := widget.NewEntry()
	downloadDir.SetText(strings.TrimSpace(svc.DownloadDir()))
	downloadDir.SetPlaceHolder("Download-Basisordner")

	clientName := widget.NewLabel(orDash(snap.ClientName))
	clientName.Wrapping = fyne.TextWrapWord
	serverURL := widget.NewLabel("https://autofetch.de")
	pairingStatus := widget.NewLabel(settingsPairingStatus(snap))
	pairingStatus.Wrapping = fyne.TextWrapWord
	hint := widget.NewLabel("Änderungen am Download-Ordner und an der IRC-Identität werden lokal gespeichert und bei laufendem Client direkt übernommen.")
	hint.Wrapping = fyne.TextWrapWord
	//ircCfg := svc.IRCConfig()
	ircSettingsButton := widget.NewButtonWithIcon("IRC-Einstellungen öffnen", theme.SettingsIcon(), func() {
		v.openIRCSettingsDialog(w, svc)
	})
	ircSettingsHint := widget.NewLabel("IRC-Networks, NickServ und SASL werden in einem eigenen Dialog bearbeitet.")
	ircSettingsHint.Wrapping = fyne.TextWrapWord

	chooseButton := widget.NewButtonWithIcon("Ordner wählen", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				v.setActionText(err.Error())
				return
			}
			if uri == nil {
				return
			}
			downloadDir.SetText(uri.Path())
		}, w)
	})

	form := widget.NewForm(
		widget.NewFormItem("Client-Name", clientName),
		widget.NewFormItem("Server", serverURL),
		widget.NewFormItem("Status", pairingStatus),
		widget.NewFormItem("Download-Ordner", container.NewVBox(downloadDir, chooseButton, hint)),
		widget.NewFormItem("IRC", container.NewVBox(ircSettingsButton, ircSettingsHint)),
	)

	dlg := dialog.NewCustomConfirm("Einstellungen", "Speichern", "Abbrechen", container.NewPadded(form), func(ok bool) {
		if !ok {
			return
		}
		go func() {
			updatedIRC := svc.IRCConfig()
			err := svc.UpdateLocalSettings(downloadDir.Text, updatedIRC, updatedIRC.AutoRegister, updatedIRC.RegistrationEmail)
			fyne.Do(func() {
				if err != nil {
					v.setActionText(err.Error())
					return
				}
				v.setActionText("Einstellungen gespeichert.")
				v.refresh(svc)
			})
		}()
	}, w)
	dlg.Resize(fyne.NewSize(680, 520))
	dlg.Show()
}

func (v *mainView) openIRCSettingsDialog(w fyne.Window, svc *app.Service) {
	current := svc.IRCConfig().WithDefaults()
	networks := append([]config.IRCNetwork(nil), current.Networks...)
	selected := -1

	ensureAtLeastOne := func() {
		if len(networks) == 0 {
			selected = -1
			return
		}
		if selected < 0 || selected >= len(networks) {
			selected = 0
		}
	}
	ensureAtLeastOne()

	labelForNetwork := func(n config.IRCNetwork, idx int) string {
		host := strings.TrimSpace(n.Host)
		if host == "" {
			host = strings.TrimSpace(n.Name)
		}
		if host == "" {
			host = fmt.Sprintf("Network %d", idx+1)
		}
		return host
	}
	channelSummary := func(n config.IRCNetwork) string {
		if len(n.Channels) == 0 {
			return "ohne Channels"
		}
		if len(n.Channels) == 1 {
			return n.Channels[0]
		}
		return fmt.Sprintf("%s +%d", n.Channels[0], len(n.Channels)-1)
	}

	hostEntry := widget.NewEntry()
	nameEntry := widget.NewEntry()
	portEntry := widget.NewEntry()
	tlsCheck := widget.NewCheck("TLS verwenden", nil)
	channelsEntry := widget.NewEntry()
	channelsEntry.SetPlaceHolder("#channel1, #channel2")
	nickEntry := widget.NewEntry()
	userEntry := widget.NewEntry()
	realnameEntry := widget.NewEntry()
	generateNickBtn := widget.NewButton("Neuen Nick generieren", nil)
	nickServEnabled := widget.NewCheck("NickServ verwenden", nil)
	nickServCommand := widget.NewEntry()
	nickServPassword := widget.NewPasswordEntry()
	nickServPassword.SetPlaceHolder("Leer lassen = unverändert")
	nickServDelete := widget.NewButton("NickServ-Passwort löschen", nil)
	saslEnabled := widget.NewCheck("SASL verwenden", nil)
	saslUsername := widget.NewEntry()
	saslPassword := widget.NewPasswordEntry()
	saslPassword.SetPlaceHolder("Leer lassen = unverändert")
	saslDelete := widget.NewButton("SASL-Zugang löschen", nil)
	secretStatus := widget.NewLabel("")
	secretStatus.Wrapping = fyne.TextWrapWord
	defaultNick := widget.NewEntry()
	defaultNick.SetText(strings.TrimSpace(current.DefaultNick))
	defaultNick.SetPlaceHolder("z. B. silentFalcon42")
	generateDefaultNick := widget.NewButton("Neu generieren", func() {
		defaultNick.SetText(internalirc.GenerateDefaultNick())
	})
	autoRegisterCheck := widget.NewCheck("Nick bei Bedarf automatisch registrieren", nil)
	autoRegisterCheck.SetChecked(current.AutoRegister)
	registrationEmail := widget.NewEntry()
	registrationEmail.SetText(strings.TrimSpace(current.RegistrationEmail))
	registrationEmail.SetPlaceHolder("E-Mail-Adresse für Nick-Registrierung")
	reverseDCCCheck := widget.NewCheck("Reverse-/Passive-DCC-Angebote annehmen", nil)
	reverseDCCCheck.SetChecked(current.ReverseDCCEnabled)
	reverseDCCPortMin := widget.NewEntry()
	reverseDCCPortMin.SetText(fmt.Sprintf("%d", current.ReverseDCCPortMin))
	reverseDCCPortMin.SetPlaceHolder("z. B. 36080")
	reverseDCCPortMax := widget.NewEntry()
	reverseDCCPortMax.SetText(fmt.Sprintf("%d", current.ReverseDCCPortMax))
	reverseDCCPortMax.SetPlaceHolder("z. B. 36090")
	reverseDCCHint := widget.NewLabel("Nur für Bots nötig, die Reverse-/Passive-DCC verwenden. Der eingestellte TCP-Portbereich muss im Router/Firewall auf die interne IP dieses Clients weitergeleitet werden.")
	reverseDCCHint.Wrapping = fyne.TextWrapWord
	globalHint := widget.NewLabel("Diese IRC-Einstellungen werden nur lokal gespeichert. Zugangsdaten liegen in irc-secrets.json, nicht in client.json.")
	globalHint.Wrapping = fyne.TextWrapWord
	localOnlyHint := widget.NewLabel("Networks und Channels werden aus Download-Aufträgen übernommen. Links wählst du vorhandene Einträge aus; rechts pflegst du lokale NickServ- und SASL-Daten. Passwortfelder bleiben leer, wenn bestehende Secrets unverändert bleiben sollen.")
	localOnlyHint.Wrapping = fyne.TextWrapWord
	listHint := widget.NewLabel("Vorhandene Networks")
	listHint.TextStyle = fyne.TextStyle{Bold: true}
	selectedInfo := widget.NewLabel("")
	selectedInfo.Wrapping = fyne.TextWrapWord

	loadFields := func() {
		ensureAtLeastOne()
		if selected < 0 || selected >= len(networks) {
			for _, e := range []*widget.Entry{nameEntry, hostEntry, portEntry, channelsEntry, nickEntry, userEntry, realnameEntry, nickServCommand, nickServPassword, saslUsername, saslPassword} {
				e.SetText("")
			}
			tlsCheck.SetChecked(false)
			nickServEnabled.SetChecked(false)
			saslEnabled.SetChecked(false)
			selectedInfo.SetText("Noch keine IRC-Networks vorhanden. Sie werden automatisch angelegt, sobald ein Download-Auftrag für ein neues Network ankommt.")
			secretStatus.SetText("")
			return
		}
		n := networks[selected]
		nameEntry.SetText(n.Name)
		hostEntry.SetText(n.Host)
		if n.Port > 0 {
			portEntry.SetText(fmt.Sprintf("%d", n.Port))
		} else {
			portEntry.SetText("")
		}
		tlsCheck.SetChecked(n.TLS)
		channelsEntry.SetText(strings.Join(n.Channels, ", "))
		nickEntry.SetText(n.Nick)
		userEntry.SetText(n.Username)
		realnameEntry.SetText(n.Realname)
		nickServEnabled.SetChecked(n.NickServ.Enabled)
		nickServCommand.SetText(n.NickServ.Command)
		nickServPassword.SetText("")
		saslEnabled.SetChecked(n.SASL.Enabled)
		saslUsername.SetText("")
		saslPassword.SetText("")
		selectedInfo.SetText(fmt.Sprintf("Ausgewähltes Network: %s (%s)", labelForNetwork(n, selected), channelSummary(n)))
		if strings.TrimSpace(n.NickServ.Password) != "" || strings.TrimSpace(n.SASL.Username) != "" || strings.TrimSpace(n.SASL.Password) != "" {
			secretStatus.SetText(buildIRCSecretStatus([]config.IRCNetwork{n}))
		} else {
			secretStatus.SetText("Keine lokalen Zugangsdaten für dieses Network gespeichert.")
		}
	}
	applyFields := func() {
		if selected < 0 || selected >= len(networks) {
			return
		}
		n := &networks[selected]
		n.Name = strings.TrimSpace(nameEntry.Text)
		n.Host = strings.TrimSpace(hostEntry.Text)
		if port := strings.TrimSpace(portEntry.Text); port != "" {
			var pv int
			if _, err := fmt.Sscanf(port, "%d", &pv); err == nil && pv > 0 {
				n.Port = pv
			}
		}
		n.TLS = tlsCheck.Checked
		parts := strings.Split(channelsEntry.Text, ",")
		chs := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				chs = append(chs, part)
			}
		}
		n.Channels = chs
		n.Nick = strings.TrimSpace(nickEntry.Text)
		n.Username = strings.TrimSpace(userEntry.Text)
		n.Realname = strings.TrimSpace(realnameEntry.Text)
		n.NickServ.Enabled = nickServEnabled.Checked
		n.NickServ.Command = strings.TrimSpace(nickServCommand.Text)
		if pw := strings.TrimSpace(nickServPassword.Text); pw != "" {
			n.NickServ.Password = pw
		}
		n.SASL.Enabled = saslEnabled.Checked
		if su := strings.TrimSpace(saslUsername.Text); su != "" {
			n.SASL.Username = su
		}
		if pw := strings.TrimSpace(saslPassword.Text); pw != "" {
			n.SASL.Password = pw
		}
	}

	generateNickBtn.OnTapped = func() {
		oldNick := strings.TrimSpace(nickEntry.Text)
		newNick := internalirc.GenerateDefaultNick()
		nickEntry.SetText(newNick)
		if strings.TrimSpace(userEntry.Text) == "" || strings.TrimSpace(userEntry.Text) == oldNick {
			userEntry.SetText(newNick)
		}
	}
	nickServDelete.OnTapped = func() {
		if selected >= 0 && selected < len(networks) {
			networks[selected].NickServ.Password = ""
			nickServPassword.SetText("")
			loadFields()
		}
	}
	saslDelete.OnTapped = func() {
		if selected >= 0 && selected < len(networks) {
			networks[selected].SASL.Username = ""
			networks[selected].SASL.Password = ""
			saslUsername.SetText("")
			saslPassword.SetText("")
			loadFields()
		}
	}

	networkList := widget.NewList(
		func() int { return len(networks) },
		func() fyne.CanvasObject {
			title := widget.NewLabel("Network")
			title.TextStyle = fyne.TextStyle{Bold: true}
			title.Wrapping = fyne.TextWrapWord
			subtitle := widget.NewLabel("")
			subtitle.Wrapping = fyne.TextWrapWord
			return container.NewVBox(title, subtitle)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			box := obj.(*fyne.Container)
			title := box.Objects[0].(*widget.Label)
			subtitle := box.Objects[1].(*widget.Label)
			n := networks[id]
			title.SetText(labelForNetwork(n, id))
			subtitle.SetText(channelSummary(n))
		},
	)
	networkList.OnSelected = func(id widget.ListItemID) {
		applyFields()
		selected = id
		loadFields()
	}

	refreshList := func() {
		ensureAtLeastOne()
		networkList.Refresh()
		if selected >= 0 {
			networkList.Select(selected)
		} else {
			networkList.UnselectAll()
		}
		loadFields()
	}

	globalForm := widget.NewForm(
		widget.NewFormItem("Default Nick", container.NewBorder(nil, nil, nil, generateDefaultNick, defaultNick)),
		widget.NewFormItem("Auto-Registrierung", container.NewVBox(autoRegisterCheck, widget.NewLabel("Registrierungs-E-Mail"), registrationEmail)),
		widget.NewFormItem("Reverse DCC", container.NewVBox(
			reverseDCCCheck,
			widget.NewLabel("DCC-Portbereich"),
			container.NewGridWithColumns(2, reverseDCCPortMin, reverseDCCPortMax),
			reverseDCCHint,
			globalHint,
		)),
	)

	detailForm := widget.NewForm(
		widget.NewFormItem("Anzeigename", nameEntry),
		widget.NewFormItem("Host", hostEntry),
		widget.NewFormItem("Port", portEntry),
		widget.NewFormItem("TLS", tlsCheck),
		widget.NewFormItem("Channels", channelsEntry),
		widget.NewFormItem("Nick", container.NewBorder(nil, nil, nil, generateNickBtn, nickEntry)),
		widget.NewFormItem("Username", userEntry),
		widget.NewFormItem("Realname", realnameEntry),
		widget.NewFormItem("NickServ", nickServEnabled),
		widget.NewFormItem("NickServ Command", nickServCommand),
		widget.NewFormItem("NickServ Passwort", container.NewVBox(nickServPassword, nickServDelete)),
		widget.NewFormItem("SASL", saslEnabled),
		widget.NewFormItem("SASL Username", saslUsername),
		widget.NewFormItem("SASL Passwort", container.NewVBox(saslPassword, saslDelete)),
	)

	leftPanel := container.NewBorder(
		container.NewVBox(listHint),
		widget.NewLabel("Keine manuelle Anlage nötig"),
		nil,
		nil,
		networkList,
	)
	rightPanel := container.NewVBox(localOnlyHint, selectedInfo, secretStatus, detailForm)
	split := container.NewHSplit(container.NewPadded(leftPanel), container.NewPadded(rightPanel))
	split.Offset = 0.32
	content := container.NewPadded(container.NewVBox(globalForm, widget.NewSeparator(), split))
	scroller := container.NewVScroll(content)
	scroller.SetMinSize(fyne.NewSize(900, 640))
	refreshList()

	dlg := dialog.NewCustomConfirm("IRC-Einstellungen", "Speichern", "Schließen", scroller, func(ok bool) {
		if !ok {
			return
		}
		go func() {
			applyFields()
			updatedIRC := svc.IRCConfig()
			updatedIRC.DefaultNick = strings.TrimSpace(defaultNick.Text)
			updatedIRC.AutoRegister = autoRegisterCheck.Checked
			updatedIRC.RegistrationEmail = strings.TrimSpace(registrationEmail.Text)
			updatedIRC.ReverseDCCEnabled = reverseDCCCheck.Checked
			if port := strings.TrimSpace(reverseDCCPortMin.Text); port != "" {
				var pv int
				if _, err := fmt.Sscanf(port, "%d", &pv); err == nil && pv > 0 {
					updatedIRC.ReverseDCCPortMin = pv
				}
			}
			if port := strings.TrimSpace(reverseDCCPortMax.Text); port != "" {
				var pv int
				if _, err := fmt.Sscanf(port, "%d", &pv); err == nil && pv > 0 {
					updatedIRC.ReverseDCCPortMax = pv
				}
			}
			updatedIRC.Networks = networks
			err := svc.UpdateLocalSettings(strings.TrimSpace(svc.DownloadDir()), updatedIRC, updatedIRC.AutoRegister, updatedIRC.RegistrationEmail)
			fyne.Do(func() {
				if err != nil {
					v.setActionText(err.Error())
					return
				}
				v.setActionText("IRC-Einstellungen gespeichert.")
				v.refresh(svc)
			})
		}()
	}, w)
	dlg.Resize(fyne.NewSize(920, 700))
	dlg.Show()
}

func (v *mainView) confirmRePair(w fyne.Window, svc *app.Service) {
	dialog.ShowConfirm("Client neu koppeln?", "Der Client wird vom aktuellen Konto getrennt und muss anschließend erneut gekoppelt werden.\n\nLokale Einstellungen wie der Download-Ordner bleiben erhalten.\nDie Verbindung zum Server wird zurückgesetzt.\n\nMöchtest du fortfahren?", func(ok bool) {
		if !ok {
			return
		}
		if err := svc.StartRePair(); err != nil {
			v.setActionText(err.Error())
			return
		}
		v.setActionText("Neues Pairing wurde gestartet.")
		v.refresh(svc)
	}, w)
}

func (v *mainView) setActionText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	v.overrideStatus = text
	v.statusBar.SetText(text)
	go func() {
		time.Sleep(4 * time.Second)
		fyne.Do(func() {
			if v.overrideStatus == text {
				v.overrideStatus = ""
			}
		})
	}()
}

func deriveUIState(snap observe.Snapshot) uiState {
	if !snap.Paired {
		return uiStatePairing
	}
	if snap.ActiveDownload != nil {
		return uiStateDownloading
	}
	return uiStateIdle
}

func buildStatusLine(snap observe.Snapshot) string {
	if !snap.Paired {
		if msg := strings.TrimSpace(snap.PairingStatus); msg != "" {
			return msg
		}
		return "Nicht gekoppelt"
	}
	if d := snap.ActiveDownload; d != nil {
		parts := []string{}
		if d.Total <= 0 && d.Downloaded <= 0 {
			parts = append(parts, "Verbinden")
		} else {
			parts = append(parts, "Download")
		}
		if target := strings.TrimSpace(d.Target); target != "" {
			parts = append(parts, target)
		}
		if d.Total > 0 {
			pct := (float64(d.Downloaded) / float64(d.Total)) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			parts = append(parts, fmt.Sprintf("%.0f%%", pct))
		}
		if d.SpeedBps > 0 {
			parts = append(parts, fmt.Sprintf("%s/s", formatBytes(int64(d.SpeedBps))))
		}
		if eta := formatDurationSeconds(d.ETASeconds); eta != "" {
			parts = append(parts, "ETA "+eta)
		}
		return strings.Join(parts, " · ")
	}
	if msg := strings.TrimSpace(snap.LastError); msg != "" {
		return "Fehler · " + msg
	}
	if snap.Connected {
		return "Verbunden"
	}
	if snap.Running {
		return "Verbindung wird aufgebaut"
	}
	return "Bereit"
}

func buildPairingURL(raw, code string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "https://autofetch.de/clients/new"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.TrimSpace(code) == "" {
		return u.String()
	}
	q := u.Query()
	q.Set("pairing_code", strings.TrimSpace(code))
	u.RawQuery = q.Encode()
	return u.String()
}

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

func orDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "—"
	}
	return value
}

func settingsPairingStatus(snap observe.Snapshot) string {
	switch {
	case !snap.Paired && strings.TrimSpace(snap.PairingStatus) != "":
		return snap.PairingStatus
	case !snap.Paired:
		return "Nicht gekoppelt"
	case snap.Connected:
		return "Gekoppelt und verbunden"
	default:
		return "Gekoppelt"
	}
}

func formatRemainingTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "abgelaufen"
	case d < time.Minute:
		return fmt.Sprintf("noch %d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("noch %d min", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("noch %d h", h)
		}
		return fmt.Sprintf("noch %d h %d min", h, m)
	}
}

func formatBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	u := 0
	for v >= 1024 && u < len(units)-1 {
		v /= 1024
		u++
	}
	if u == 0 {
		return fmt.Sprintf("%d %s", n, units[u])
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

func formatDurationSeconds(sec int64) string {
	if sec <= 0 {
		return ""
	}
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

func formatTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Local().Format("02.01.2006 15:04:05")
}
