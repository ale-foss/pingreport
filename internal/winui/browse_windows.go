// Package winui provides Windows UI helpers for pingreport.
package winui

import (
	"errors"
	"syscall"

	"github.com/TheTitanrain/w32"
)

// ErrCancelled is returned when the user dismisses a dialog without selecting.
var ErrCancelled = errors.New("dialog cancelled")

const bffmInitialized = 1

// browseCallback is the BrowseForFolder callback. On BFFM_INITIALIZED it
// centers the window horizontally and places it in the upper portion of the
// screen so it is never obscured by the Windows taskbar.
func browseCallback(hwnd w32.HWND, msg uint, _ uintptr, _ uintptr) int {
	if msg != bffmInitialized {
		return 0
	}

	screenW := w32.GetSystemMetrics(w32.SM_CXSCREEN)
	screenH := w32.GetSystemMetrics(w32.SM_CYSCREEN)

	rect := w32.GetWindowRect(hwnd)
	dlgW := int(rect.Right - rect.Left)
	dlgH := int(rect.Bottom - rect.Top)

	// Cap to at most 50 % of screen width and 55 % of screen height so the
	// dialog fits on small or low-resolution displays.
	if maxW := screenW * 50 / 100; dlgW > maxW {
		dlgW = maxW
	}
	if maxH := screenH * 55 / 100; dlgH > maxH {
		dlgH = maxH
	}

	// Center horizontally; place 15 % from the top so it stays well clear of
	// a bottom-anchored taskbar (typically 40–48 px on a 1080p screen).
	x := (screenW - dlgW) / 2
	y := screenH * 15 / 100

	// Safety clamp: ensure the bottom edge leaves at least 60 px for the taskbar.
	if bottom := y + dlgH; bottom > screenH-60 {
		y = screenH - 60 - dlgH
	}
	if y < 0 {
		y = 0
	}

	w32.SetWindowPos(hwnd, 0, x, y, dlgW, dlgH, w32.SWP_NOZORDER)
	return 0
}

// BrowseForFolder shows a folder-picker dialog that is centered near the top
// of the primary screen, ensuring it is never hidden by the Windows taskbar.
// Returns ErrCancelled if the user closes the dialog without selecting.
func BrowseForFolder(title string) (string, error) {
	bi := &w32.BROWSEINFO{
		Flags:        w32.BIF_RETURNONLYFSDIRS | w32.BIF_NEWDIALOGSTYLE,
		CallbackFunc: syscall.NewCallback(browseCallback),
	}
	if title != "" {
		bi.Title, _ = syscall.UTF16PtrFromString(title)
	}

	res := w32.SHBrowseForFolder(bi)
	if res == 0 {
		return "", ErrCancelled
	}
	return w32.SHGetPathFromIDList(res), nil
}
