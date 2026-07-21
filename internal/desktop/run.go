package desktop

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/config"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/localization"

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
	l := svc.Localizer()
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
		view.setActionText(l.T("notice.window_minimized"))
	})

	if desk, ok := a.(desktopdriver.App); ok {
		openItem := fyne.NewMenuItem(l.T("tray.open_window"), func() {
			w.Show()
			w.RequestFocus()
		})
		startItem := fyne.NewMenuItem(l.T("action.start"), func() {
			view.startOrResume(svc)
		})
		pauseItem := fyne.NewMenuItem(l.T("action.pause"), func() {
			view.stopOrPause(svc)
		})
		settingsItem := fyne.NewMenuItem(l.T("action.settings"), func() {
			view.openSettingsDialog(w, svc)
		})
		repairPairingItem := fyne.NewMenuItem(l.T("action.repair"), func() {
			view.confirmRePair(w, svc)
		})
		versionItem := fyne.NewMenuItem("Version "+svc.BuildInfo().Version, func() {})
		versionItem.Disabled = true
		quitItem := fyne.NewMenuItem(l.T("action.quit"), func() {
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
			versionItem,
			quitItem,
		)
		view.trayApp = desk
		desk.SetSystemTrayIcon(assets.TrayIconPending)
		desk.SetSystemTrayMenu(menu)
	}

	if err := svc.Start(); err != nil {
		view.setActionText(l.UserError(err.Error()))
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
	l              *localization.Localizer
}

func newMainView(w fyne.Window, svc *app.Service) *mainView {
	v := &mainView{lastState: uiState(-1), l: svc.Localizer()}
	v.titleLabel = widget.NewLabelWithStyle("autofetch-client", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.statusBar = widget.NewLabel("")
	v.statusBar.Wrapping = fyne.TextTruncate
	v.contentHolder = container.NewMax()

	v.primaryAction = widget.NewButtonWithIcon(v.l.T("action.start"), theme.MediaPlayIcon(), func() {
		v.startOrResume(svc)
	})
	v.secondaryAction = widget.NewButtonWithIcon(v.l.T("action.pause"), theme.MediaPauseIcon(), func() {
		v.stopOrPause(svc)
	})
	v.settingsAction = widget.NewButtonWithIcon(v.l.T("action.settings"), theme.SettingsIcon(), func() {
		v.openSettingsDialog(w, svc)
	})
	v.repairPairingAction = widget.NewButtonWithIcon(v.l.T("action.repair"), theme.ViewRefreshIcon(), func() {
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
	v.idleMessage = widget.NewLabelWithStyle(v.l.T("idle.message"), fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	v.idleMessage.Wrapping = fyne.TextWrapWord

	copyBtn := widget.NewButtonWithIcon(v.l.T("action.copy_code"), theme.ContentCopyIcon(), func() {
		snap := svc.Snapshot()
		code := strings.TrimSpace(snap.PairingCode)
		if code == "" {
			v.setActionText(v.l.T("notice.no_pairing_code"))
			return
		}
		w.Clipboard().SetContent(code)
		v.setActionText(v.l.T("notice.pairing_code_copied"))
	})
	openBtn := widget.NewButtonWithIcon(v.l.T("action.open_pairing_page"), theme.ComputerIcon(), func() {
		snap := svc.Snapshot()
		target := buildPairingURL(snap.PairingURL, snap.PairingCode)
		if err := fyne.CurrentApp().OpenURL(mustURL(target)); err != nil {
			v.setActionText(v.l.UserError(err.Error()))
			return
		}
		v.setActionText(v.l.T("notice.pairing_page_opened"))
	})
	pairButtons := container.NewGridWithColumns(2, copyBtn, openBtn)
	pairContent := container.NewVBox(
		widget.NewLabel(v.l.T("pairing.enter_code")),
		container.NewPadded(container.NewCenter(container.NewHBox(v.pairCode))),
		v.pairExpiry,
		pairButtons,
	)
	v.pairingCard = widget.NewCard(v.l.T("pairing.card_title"), "", pairContent)

	v.logEntry = widget.NewMultiLineEntry()
	v.logEntry.Wrapping = fyne.TextWrapWord
	v.logEntry.SetMinRowsVisible(12)
	logScroll := container.NewVScroll(v.logEntry)
	logBg := canvas.NewRectangle(theme.InputBackgroundColor())
	v.logAccordion = widget.NewAccordion(widget.NewAccordionItem(v.l.T("log.show"), container.NewStack(logBg, container.NewPadded(logScroll))))

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
	v.statusBar.SetText(buildStatusLine(v.l, snap))
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
		v.primaryAction.SetText(v.l.T("action.start"))
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.SetText(v.l.T("action.pause"))
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
		return
	}

	switch {
	case snap.ActiveDownload != nil:
		v.primaryAction.Disable()
		v.primaryAction.SetText(v.l.T("action.start"))
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Enable()
		v.secondaryAction.SetText(v.l.T("action.pause"))
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
	case snap.Running:
		v.primaryAction.Disable()
		v.primaryAction.SetText(v.l.T("action.start"))
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Enable()
		v.secondaryAction.SetText(v.l.T("action.pause"))
		v.secondaryAction.SetIcon(theme.MediaPauseIcon())
	default:
		label := v.l.T("action.start")
		if strings.TrimSpace(snap.LastError) != "" {
			label = v.l.T("action.resume")
		}
		v.primaryAction.Enable()
		v.primaryAction.SetText(label)
		v.primaryAction.SetIcon(theme.MediaPlayIcon())
		v.secondaryAction.Disable()
		v.secondaryAction.SetText(v.l.T("action.pause"))
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

	expires := v.l.FormatTimestamp(snap.PairingExpiry)
	remaining := v.l.FormatRemaining(snap.PairingExpiry)
	switch {
	case expires != "" && remaining != "":
		v.pairExpiry.SetText(v.l.T("pairing.valid_until", map[string]any{"Expires": expires, "Remaining": remaining}))
	case expires != "":
		v.pairExpiry.SetText(v.l.T("pairing.valid_until_no_remaining", map[string]any{"Expires": expires}))
	default:
		v.pairExpiry.SetText(v.l.T("pairing.validity_checking"))
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
		v.setActionText(v.l.UserError(err.Error()))
		return
	}
	v.setActionText(v.l.T("notice.client_started"))
	v.refresh(svc)
}

func (v *mainView) stopOrPause(svc *app.Service) {
	if err := svc.Stop(); err != nil {
		v.setActionText(v.l.UserError(err.Error()))
		return
	}
	v.setActionText(v.l.T("notice.client_paused"))
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

func buildIRCSecretStatus(l *localization.Localizer, networks []config.IRCNetwork) string {
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
			status = append(status, l.T("irc.nickserv_saved"))
		}
		if strings.TrimSpace(n.SASL.Username) != "" || strings.TrimSpace(n.SASL.Password) != "" {
			status = append(status, l.T("irc.sasl_saved"))
		}
		if len(status) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", host, strings.Join(status, ", ")))
	}
	if len(lines) == 0 {
		return l.T("irc.no_credentials")
	}
	return l.T("irc.saved_credentials", map[string]any{"Lines": strings.Join(lines, "\n- ")})
}

func (v *mainView) openSettingsDialog(w fyne.Window, svc *app.Service) {
	snap := svc.Snapshot()
	downloadDir := widget.NewEntry()
	downloadDir.SetText(strings.TrimSpace(svc.DownloadDir()))
	downloadDir.SetPlaceHolder(v.l.T("settings.download_folder_placeholder"))

	clientName := widget.NewLabel(orDash(snap.ClientName))
	clientName.Wrapping = fyne.TextWrapWord
	serverURL := widget.NewLabel("https://autofetch.de")
	pairingStatus := widget.NewLabel(settingsPairingStatus(v.l, snap))
	pairingStatus.Wrapping = fyne.TextWrapWord
	hint := widget.NewLabel(v.l.T("settings.local_changes_hint"))
	hint.Wrapping = fyne.TextWrapWord
	//ircCfg := svc.IRCConfig()
	ircSettingsButton := widget.NewButtonWithIcon(v.l.T("action.open_irc_settings"), theme.SettingsIcon(), func() {
		v.openIRCSettingsDialog(w, svc)
	})
	ircSettingsHint := widget.NewLabel(v.l.T("settings.irc_hint"))
	ircSettingsHint.Wrapping = fyne.TextWrapWord

	chooseButton := widget.NewButtonWithIcon(v.l.T("action.choose_folder"), theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				v.setActionText(v.l.UserError(err.Error()))
				return
			}
			if uri == nil {
				return
			}
			downloadDir.SetText(uri.Path())
		}, w)
	})

	form := widget.NewForm(
		widget.NewFormItem(v.l.T("settings.client_name"), clientName),
		widget.NewFormItem(v.l.T("settings.server"), serverURL),
		widget.NewFormItem(v.l.T("settings.status"), pairingStatus),
		widget.NewFormItem(v.l.T("settings.download_folder"), container.NewVBox(downloadDir, chooseButton, hint)),
		widget.NewFormItem("IRC", container.NewVBox(ircSettingsButton, ircSettingsHint)),
	)

	dlg := dialog.NewCustomConfirm(v.l.T("settings.title"), v.l.T("action.save"), v.l.T("action.cancel"), container.NewPadded(form), func(ok bool) {
		if !ok {
			return
		}
		go func() {
			updatedIRC := svc.IRCConfig()
			err := svc.UpdateLocalSettings(downloadDir.Text, updatedIRC, updatedIRC.AutoRegister, updatedIRC.RegistrationEmail)
			fyne.Do(func() {
				if err != nil {
					v.setActionText(v.l.UserError(err.Error()))
					return
				}
				v.setActionText(v.l.T("notice.settings_saved"))
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
			host = fmt.Sprintf("%s %d", v.l.T("irc.network"), idx+1)
		}
		return host
	}
	channelSummary := func(n config.IRCNetwork) string {
		if len(n.Channels) == 0 {
			return v.l.T("irc.no_channels")
		}
		if len(n.Channels) == 1 {
			return n.Channels[0]
		}
		return fmt.Sprintf("%s +%d", n.Channels[0], len(n.Channels)-1)
	}

	hostEntry := widget.NewEntry()
	nameEntry := widget.NewEntry()
	portEntry := widget.NewEntry()
	tlsCheck := widget.NewCheck(v.l.T("irc.use_tls"), nil)
	channelsEntry := widget.NewEntry()
	channelsEntry.SetPlaceHolder("#channel1, #channel2")
	nickEntry := widget.NewEntry()
	userEntry := widget.NewEntry()
	realnameEntry := widget.NewEntry()
	generateNickBtn := widget.NewButton(v.l.T("action.generate_nick"), nil)
	nickServEnabled := widget.NewCheck(v.l.T("irc.use_nickserv"), nil)
	nickServCommand := widget.NewEntry()
	nickServPassword := widget.NewPasswordEntry()
	nickServPassword.SetPlaceHolder(v.l.T("irc.leave_unchanged"))
	nickServDelete := widget.NewButton(v.l.T("action.delete_nickserv_password"), nil)
	saslEnabled := widget.NewCheck(v.l.T("irc.use_sasl"), nil)
	saslUsername := widget.NewEntry()
	saslPassword := widget.NewPasswordEntry()
	saslPassword.SetPlaceHolder(v.l.T("irc.leave_unchanged"))
	saslDelete := widget.NewButton(v.l.T("action.delete_sasl_access"), nil)
	secretStatus := widget.NewLabel("")
	secretStatus.Wrapping = fyne.TextWrapWord
	defaultNick := widget.NewEntry()
	defaultNick.SetText(strings.TrimSpace(current.DefaultNick))
	defaultNick.SetPlaceHolder(v.l.T("irc.default_nick_example"))
	generateDefaultNick := widget.NewButton(v.l.T("action.generate_new"), func() {
		defaultNick.SetText(internalirc.GenerateDefaultNick())
	})
	autoRegisterCheck := widget.NewCheck(v.l.T("irc.auto_register_if_required"), nil)
	autoRegisterCheck.SetChecked(current.AutoRegister)
	registrationEmail := widget.NewEntry()
	registrationEmail.SetText(strings.TrimSpace(current.RegistrationEmail))
	registrationEmail.SetPlaceHolder(v.l.T("irc.registration_email_placeholder"))
	reverseDCCCheck := widget.NewCheck(v.l.T("irc.accept_reverse_dcc"), nil)
	reverseDCCCheck.SetChecked(current.ReverseDCCEnabled)
	reverseDCCPortMin := widget.NewEntry()
	reverseDCCPortMin.SetText(fmt.Sprintf("%d", current.ReverseDCCPortMin))
	reverseDCCPortMin.SetPlaceHolder(v.l.T("irc.port_min_example"))
	reverseDCCPortMax := widget.NewEntry()
	reverseDCCPortMax.SetText(fmt.Sprintf("%d", current.ReverseDCCPortMax))
	reverseDCCPortMax.SetPlaceHolder(v.l.T("irc.port_max_example"))
	reverseDCCHint := widget.NewLabel(v.l.T("irc.reverse_dcc_hint"))
	reverseDCCHint.Wrapping = fyne.TextWrapWord
	globalHint := widget.NewLabel(v.l.T("irc.local_storage_hint"))
	globalHint.Wrapping = fyne.TextWrapWord
	localOnlyHint := widget.NewLabel(v.l.T("irc.local_only_hint"))
	localOnlyHint.Wrapping = fyne.TextWrapWord
	listHint := widget.NewLabel(v.l.T("irc.existing_networks"))
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
			selectedInfo.SetText(v.l.T("irc.no_networks"))
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
		selectedInfo.SetText(v.l.T("irc.selected_network", map[string]any{"Network": labelForNetwork(n, selected), "Channels": channelSummary(n)}))
		if strings.TrimSpace(n.NickServ.Password) != "" || strings.TrimSpace(n.SASL.Username) != "" || strings.TrimSpace(n.SASL.Password) != "" {
			secretStatus.SetText(buildIRCSecretStatus(v.l, []config.IRCNetwork{n}))
		} else {
			secretStatus.SetText(v.l.T("irc.no_credentials_for_network"))
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
			title := widget.NewLabel(v.l.T("irc.network"))
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
		widget.NewFormItem(v.l.T("irc.default_nick"), container.NewBorder(nil, nil, nil, generateDefaultNick, defaultNick)),
		widget.NewFormItem(v.l.T("irc.auto_registration"), container.NewVBox(autoRegisterCheck, widget.NewLabel(v.l.T("irc.registration_email")), registrationEmail)),
		widget.NewFormItem("Reverse DCC", container.NewVBox(
			reverseDCCCheck,
			widget.NewLabel(v.l.T("irc.dcc_port_range")),
			container.NewGridWithColumns(2, reverseDCCPortMin, reverseDCCPortMax),
			reverseDCCHint,
			globalHint,
		)),
	)

	detailForm := widget.NewForm(
		widget.NewFormItem(v.l.T("irc.display_name"), nameEntry),
		widget.NewFormItem(v.l.T("irc.host"), hostEntry),
		widget.NewFormItem(v.l.T("irc.port"), portEntry),
		widget.NewFormItem("TLS", tlsCheck),
		widget.NewFormItem(v.l.T("irc.channels"), channelsEntry),
		widget.NewFormItem(v.l.T("irc.nick"), container.NewBorder(nil, nil, nil, generateNickBtn, nickEntry)),
		widget.NewFormItem(v.l.T("irc.username"), userEntry),
		widget.NewFormItem(v.l.T("irc.realname"), realnameEntry),
		widget.NewFormItem("NickServ", nickServEnabled),
		widget.NewFormItem(v.l.T("irc.nickserv_command"), nickServCommand),
		widget.NewFormItem(v.l.T("irc.nickserv_password"), container.NewVBox(nickServPassword, nickServDelete)),
		widget.NewFormItem("SASL", saslEnabled),
		widget.NewFormItem(v.l.T("irc.sasl_username"), saslUsername),
		widget.NewFormItem(v.l.T("irc.sasl_password"), container.NewVBox(saslPassword, saslDelete)),
	)

	leftPanel := container.NewBorder(
		container.NewVBox(listHint),
		widget.NewLabel(v.l.T("irc.no_manual_creation")),
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

	dlg := dialog.NewCustomConfirm(v.l.T("irc.title"), v.l.T("action.save"), v.l.T("action.close"), scroller, func(ok bool) {
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
					v.setActionText(v.l.UserError(err.Error()))
					return
				}
				v.setActionText(v.l.T("notice.irc_settings_saved"))
				v.refresh(svc)
			})
		}()
	}, w)
	dlg.Resize(fyne.NewSize(920, 700))
	dlg.Show()
}

func (v *mainView) confirmRePair(w fyne.Window, svc *app.Service) {
	dialog.ShowConfirm(v.l.T("repair.title"), v.l.T("repair.message"), func(ok bool) {
		if !ok {
			return
		}
		if err := svc.StartRePair(); err != nil {
			v.setActionText(v.l.UserError(err.Error()))
			return
		}
		v.setActionText(v.l.T("notice.repair_started"))
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

func buildStatusLine(l *localization.Localizer, snap observe.Snapshot) string {
	if !snap.Paired {
		if code := strings.TrimSpace(snap.PairingStatus); code != "" {
			return l.Status(code)
		}
		return l.T("status.not_paired")
	}
	if d := snap.ActiveDownload; d != nil {
		parts := []string{}
		if d.Total <= 0 && d.Downloaded <= 0 {
			parts = append(parts, l.T("status.connect_download"))
		} else {
			parts = append(parts, l.T("status.download"))
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
		if eta := l.FormatDurationSeconds(d.ETASeconds); eta != "" {
			parts = append(parts, "ETA "+eta)
		}
		return strings.Join(parts, " · ")
	}
	if msg := strings.TrimSpace(snap.LastError); msg != "" {
		return l.T("status.error", map[string]any{"Message": l.UserError(msg)})
	}
	if snap.Connected {
		return l.T("status.connected")
	}
	if snap.Running {
		return l.T("status.connecting")
	}
	return l.T("status.ready")
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

func settingsPairingStatus(l *localization.Localizer, snap observe.Snapshot) string {
	switch {
	case !snap.Paired && strings.TrimSpace(snap.PairingStatus) != "":
		return l.Status(snap.PairingStatus)
	case !snap.Paired:
		return l.T("status.not_paired")
	case snap.Connected:
		return l.T("status.paired_connected")
	default:
		return l.T("status.paired")
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
