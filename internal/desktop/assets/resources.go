package assets

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

var (
	//go:embed autofetch_af.svg
	appIconSVG []byte
	//go:embed autofetch_af_dark.svg
	appIconDarkSVG []byte
	//go:embed autofetch_af_okay.svg
	trayOkaySVG []byte
	//go:embed autofetch_af_pending.svg
	trayPendingSVG []byte
	//go:embed autofetch_af_fail.svg
	trayFailSVG []byte
	//go:embed app_icon.png
	appIconPNG []byte
)

var (
	AppIcon         = fyne.NewStaticResource("autofetch_af.svg", appIconSVG)
	AppIconDark     = fyne.NewStaticResource("autofetch_af_dark.svg", appIconDarkSVG)
	AppIconPNG      = fyne.NewStaticResource("app_icon.png", appIconPNG)
	TrayIconOkay    = fyne.NewStaticResource("autofetch_af_okay.svg", trayOkaySVG)
	TrayIconPending = fyne.NewStaticResource("autofetch_af_pending.svg", trayPendingSVG)
	TrayIconFail    = fyne.NewStaticResource("autofetch_af_fail.svg", trayFailSVG)
)
