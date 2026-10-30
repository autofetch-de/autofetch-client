package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/archive"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

// Activity is a local, read-only view of the client's technical work. The
// actions deliberately do not modify files or remote server jobs: hiding a
// receipt only changes its presentation; retry affects only archive extraction.
func (v *mainView) openActivityWindow(svc *app.Service) {
	if v.activityWindow != nil {
		v.activityWindow.Show()
		v.activityWindow.RequestFocus()
		v.refreshActivityWindow(svc)
		return
	}
	w := fyne.CurrentApp().NewWindow(v.l.T("activity.title"))
	w.Resize(fyne.NewSize(790, 610))
	v.activityWindow = w
	v.activityHistory = container.NewVBox()
	v.activityArchives = container.NewVBox()
	v.activityLogs = widget.NewMultiLineEntry()
	v.activityLogs.Wrapping = fyne.TextWrapWord
	v.activityLogs.SetMinRowsVisible(12)
	v.activityMessage = widget.NewLabel("")
	v.activityMessage.Wrapping = fyne.TextWrapWord

	historyScroller := container.NewVScroll(v.activityHistory)
	historyScroller.SetMinSize(fyne.NewSize(680, 190))
	archiveScroller := container.NewVScroll(v.activityArchives)
	archiveScroller.SetMinSize(fyne.NewSize(680, 190))
	logScroller := container.NewVScroll(v.activityLogs)
	logScroller.SetMinSize(fyne.NewSize(680, 220))

	clearHistory := widget.NewButton(v.l.T("history.clear"), func() {
		dialog.ShowConfirm(v.l.T("history.clear"), v.l.T("history.clear_confirm"), func(yes bool) {
			if !yes {
				return
			}
			if err := svc.HideDownloadHistory(context.Background(), "", true); err != nil {
				v.activityMessage.SetText(v.l.T("history.hide_failed") + " " + v.l.UserError(err.Error()))
				return
			}
			v.activityMessage.SetText(v.l.T("history.hide_ok"))
			v.refreshActivityWindow(svc)
		}, w)
	})
	historyArea := container.NewBorder(
		container.NewVBox(
			widget.NewLabel(v.l.T("history.presentation_only")),
			container.NewHBox(clearHistory),
		), nil, nil, nil, historyScroller,
	)
	archiveArea := container.NewBorder(widget.NewLabel(v.l.T("history.local_only")), nil, nil, nil, archiveScroller)

	panels := widget.NewAccordion(
		widget.NewAccordionItem(v.l.T("history.downloads_title"), historyArea),
		widget.NewAccordionItem(v.l.T("history.archives_title"), archiveArea),
		widget.NewAccordionItem(v.l.T("log.show"), logScroller),
	)
	panels.MultiOpen = false
	refresh := widget.NewButton(v.l.T("activity.refresh"), func() { v.refreshActivityWindow(svc) })
	content := container.NewBorder(
		container.NewVBox(container.NewHBox(refresh), v.activityMessage),
		nil, nil, nil,
		container.NewVScroll(panels),
	)
	w.SetContent(container.NewPadded(content))
	w.SetOnClosed(func() {
		v.activityWindow = nil
		v.activityHistory = nil
		v.activityArchives = nil
		v.activityLogs = nil
		v.activityMessage = nil
	})
	v.refreshActivityWindow(svc)
	w.Show()
}

func (v *mainView) refreshActivityWindow(svc *app.Service) {
	if v.activityWindow == nil || v.activityHistory == nil || v.activityArchives == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	items, err := svc.DownloadHistory(ctx, 50)
	if err != nil {
		v.activityHistory.Objects = []fyne.CanvasObject{widget.NewLabel(v.l.T("history.error") + ": " + v.l.UserError(err.Error()))}
	} else {
		v.activityHistory.Objects = v.historyRows(svc, items)
	}
	v.activityHistory.Refresh()
	archives, err := svc.ArchiveHistory(ctx, 50)
	if err != nil {
		v.activityArchives.Objects = []fyne.CanvasObject{widget.NewLabel(v.l.T("history.error") + ": " + v.l.UserError(err.Error()))}
	} else {
		v.activityArchives.Objects = v.archiveRows(svc, archives)
	}
	v.activityArchives.Refresh()
	if v.activityLogs != nil {
		logText := strings.Join(svc.Snapshot().Logs, "\n")
		if v.activityLogs.Text != logText {
			v.activityLogs.SetText(logText)
		}
	}
}

func (v *mainView) historyRows(svc *app.Service, items []worker.DownloadHistoryItem) []fyne.CanvasObject {
	if len(items) == 0 {
		return []fyne.CanvasObject{widget.NewLabel(v.l.T("history.empty"))}
	}
	rows := make([]fyne.CanvasObject, 0, len(items))
	for _, item := range items {
		item := item
		filename := widget.NewLabel(orDash(item.Filename))
		filename.TextStyle = fyne.TextStyle{Bold: true}
		filename.Wrapping = fyne.TextTruncate
		details := widget.NewLabel(fmt.Sprintf("%s · %s · %s: %d%s", item.Phase, formatBytes(item.Bytes), v.l.T("history.attempts"), item.AttemptCount, activityErrorSuffix(item.LastErrorCode)))
		details.Wrapping = fyne.TextTruncate
		hide := widget.NewButton(v.l.T("history.remove"), func() {
			dialog.ShowConfirm(v.l.T("history.remove"), v.l.T("history.remove_confirm"), func(yes bool) {
				if !yes {
					return
				}
				if err := svc.HideDownloadHistory(context.Background(), item.JobID, false); err != nil {
					v.activityMessage.SetText(v.l.T("history.hide_failed") + " " + v.l.UserError(err.Error()))
					return
				}
				v.refreshActivityWindow(svc)
			}, v.activityWindow)
		})
		rows = append(rows, container.NewBorder(nil, nil, nil, hide, container.NewVBox(filename, details)))
	}
	return rows
}

func (v *mainView) archiveRows(svc *app.Service, items []archive.Item) []fyne.CanvasObject {
	if len(items) == 0 {
		return []fyne.CanvasObject{widget.NewLabel(v.l.T("history.empty"))}
	}
	rows := make([]fyne.CanvasObject, 0, len(items))
	for _, item := range items {
		item := item
		filename := widget.NewLabel(orDash(filepath.Base(item.Source)))
		filename.TextStyle = fyne.TextStyle{Bold: true}
		filename.Wrapping = fyne.TextTruncate
		progress := ""
		if item.ProgressTotalBytes > 0 && item.Status == "extracting" {
			pct := float64(item.ProgressBytes) / float64(item.ProgressTotalBytes) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			progress = fmt.Sprintf(" · %.0f%%", pct)
		}
		status := widget.NewLabel(item.Status + activityErrorSuffix(item.ErrorCode) + progress)
		status.Wrapping = fyne.TextTruncate
		var action fyne.CanvasObject
		if item.Status == "failed" {
			action = widget.NewButton(v.l.T("history.retry"), func() {
				dialog.ShowConfirm(v.l.T("history.retry"), v.l.T("history.retry_confirm"), func(yes bool) {
					if !yes {
						return
					}
					if err := svc.RetryArchive(context.Background(), item.JobID); err != nil {
						v.activityMessage.SetText(v.l.T("history.retry_failed") + " " + v.l.UserError(err.Error()))
						return
					}
					v.activityMessage.SetText(v.l.T("history.retry_ok"))
					v.refreshActivityWindow(svc)
				}, v.activityWindow)
			})
		}
		rows = append(rows, container.NewBorder(nil, nil, nil, action, container.NewVBox(filename, status)))
	}
	return rows
}

func activityErrorSuffix(code string) string {
	if code == "" {
		return ""
	}
	return " · " + code
}
