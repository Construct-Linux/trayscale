package ui

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"codeberg.org/puregotk/puregotk/v4/adw"
	"codeberg.org/puregotk/puregotk/v4/gio"
	"codeberg.org/puregotk/puregotk/v4/glib"
	"codeberg.org/puregotk/puregotk/v4/gobject"
	"codeberg.org/puregotk/puregotk/v4/gtk"
	"deedles.dev/trayscale/internal/gutil"
	"deedles.dev/trayscale/internal/locale"
	"deedles.dev/trayscale/internal/tsutil"
	"tailscale.com/tailcfg"
)

// tsTimeout bounds a request to tailscaled made from the UI.
const tsTimeout = 30 * time.Second

// goTS runs call off the main thread with tsTimeout, as setStatus does,
// so that a slow or stuck tailscaled can't freeze the window, and then
// done, if it isn't nil, on the main thread with call's error. keep
// stays alive until done has run, in case its window is closed in the
// meantime.
func goTS(keep *gobject.Object, what string, call func(context.Context) error, done func(error)) {
	if keep != nil {
		keep.Ref()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), tsTimeout)
		defer cancel()

		err := call(ctx)
		if err != nil {
			slog.Error(what, "err", err)
		}
		gutil.IdleAdd(func() {
			if done != nil {
				done(err)
			}
			if keep != nil {
				keep.Unref()
			}
		})
	}()
}

// applySwitch applies s with set through goTS. The status updates that
// follow set the switch; if set fails, it's turned back.
func applySwitch(sw *gtk.Switch, what string, s bool, set func(context.Context, bool) error) {
	goTS(&sw.Object, what, func(ctx context.Context) error {
		return set(ctx, s)
	}, func(err error) {
		if err != nil {
			sw.SetActive(!s)
		}
	})
}

// setExitNode uses the peer with id as the exit node if s is true, or
// stops using one if it's false. A machine can't use an exit node while
// it advertises itself as one, so that's turned off first.
func setExitNode(ctx context.Context, id tailcfg.StableNodeID, s bool) error {
	if !s {
		return tsutil.DisableExitNode(ctx)
	}
	if err := tsutil.AdvertiseExitNode(ctx, false); err != nil {
		slog.Error("disable exit node advertisement", "err", err)
		// Continue anyways.
	}
	return tsutil.ExitNode(ctx, id)
}

// addPageAction adds action to group, connecting activate, and drops
// the caller's reference to action.
func addPageAction(group *gio.SimpleActionGroup, action *gio.SimpleAction, activate func(param *glib.Variant)) {
	gutil.Connect(action, action.ConnectActivate, func(_ gio.SimpleAction, p uintptr) {
		var param *glib.Variant
		if p != 0 {
			param = gutil.Ptr[glib.Variant](p)
		}
		activate(param)
	})
	group.AddAction(action)
	action.Unref()
}

// rowSwitch returns the GtkSwitch of a switch row, borrowed from it.
func rowSwitch(row *adw.SwitchRow) *gtk.Switch {
	w := row.GetActivatableWidget()
	w.Unref() // The row keeps it alive.
	return gutil.MustCast[gtk.Switch](w, gtk.SwitchGLibType())
}

// setSwitch sets both the state and the active property of a switch
// row's switch.
func setSwitch(row *adw.SwitchRow, v bool) {
	sw := rowSwitch(row)
	sw.SetState(v)
	sw.SetActive(v)
}

// setPlaceholder gives lb a placeholder row titled title.
func setPlaceholder(lb *gtk.ListBox, title string) {
	row := adw.NewActionRow()
	row.SetTitle(title)
	lb.SetPlaceholder(&row.Widget)
	row.Unref()
}

// copyRow returns a new row showing text with a button that copies
// it. The caller owns the row.
func (a *App) copyRow(text string) *gtk.Widget {
	copyButton := gtk.NewButtonFromIconName("edit-copy-symbolic")
	copyButton.SetMarginTop(12) // Why is this necessary?
	copyButton.SetMarginBottom(12)
	copyButton.SetHasFrame(false)
	copyButton.SetTooltipText(locale.Get("Copy to Clipboard"))
	gutil.Connect(copyButton, copyButton.ConnectClicked, func(gtk.Button) {
		a.clip(text)
		a.win.Toast(locale.Get("Copied to clipboard"))
	})

	row := adw.NewActionRow()
	row.SetTitleSelectable(true)
	row.AddSuffix(&copyButton.Widget)
	row.SetActivatableWidget(&copyButton.Widget)
	copyButton.Unref()
	row.SetTitle(text)
	return &row.Widget
}

// goPushFile pushes the GFile obj, which is borrowed, to peer in a new
// goroutine, holding a reference to it until the push is done.
func (a *App) goPushFile(peer tailcfg.StableNodeID, obj gobject.Ptr) {
	file := &gio.FileBase{Ptr: obj.GoPointer()}
	gutil.Obj(file).Ref()
	// Released by the goroutine.
	go func() {
		a.pushFile(context.TODO(), peer, file)
		gutil.IdleAdd(gutil.Obj(file).Unref)
	}()
}

func routeActionRow(route netip.Prefix) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitleSelectable(true)
	row.SetTitle(route.String())
	return row
}

var listBoxSort gtk.ListBoxSortFunc = func(r1, r2, id uintptr) int32 {
	f := gutil.Held[func(r1, r2 *gtk.ListBoxRow) int](id)
	return int32(f(gtk.ListBoxRowNewFromInternalPtr(r1), gtk.ListBoxRowNewFromInternalPtr(r2)))
}

// setSortFunc sets lb's sort function. It's released when GTK drops it.
func setSortFunc(lb *gtk.ListBox, f func(r1, r2 *gtk.ListBoxRow) int) {
	lb.SetSortFunc(&listBoxSort, gutil.Hold(f), &gutil.FreeData)
}

var listBoxFilter gtk.ListBoxFilterFunc = func(row, id uintptr) bool {
	return gutil.Held[func(*gtk.ListBoxRow) bool](id)(gtk.ListBoxRowNewFromInternalPtr(row))
}

// setFilterFunc sets lb's filter function, releasing it like
// setSortFunc.
func setFilterFunc(lb *gtk.ListBox, f func(row *gtk.ListBoxRow) bool) {
	lb.SetFilterFunc(&listBoxFilter, gutil.Hold(f), &gutil.FreeData)
}
