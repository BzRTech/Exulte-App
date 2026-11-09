//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetForeground      = user32.NewProc("SetForegroundWindow")
	procIsIconic           = user32.NewProc("IsIconic")
	procAllowSetForeground = user32.NewProc("AllowSetForegroundWindow")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procSetExecutionState  = kernel32.NewProc("SetThreadExecutionState")
)

var (
	uiMu   sync.Mutex
	uiView webview2.WebView
)

func acquireSingleInstance() (release func(), first bool) {
	name, _ := windows.UTF16PtrFromString(`Local\DeixaMesaDeAudio`)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return func() {}, false
	}
	return func() {
		if h != 0 {
			windows.CloseHandle(h)
		}
	}, true
}

func allowForeground() {
	procAllowSetForeground.Call(^uintptr(0)) // ASFW_ANY
}

func bringToFront() {
	uiMu.Lock()
	w := uiView
	uiMu.Unlock()
	if w == nil {
		return
	}
	w.Dispatch(func() {
		hwnd := uintptr(w.Window())
		if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
			procShowWindow.Call(hwnd, 9) // SW_RESTORE
		}
		procSetForeground.Call(hwnd)
	})
}

func runUI(url string, pending *pendingQueue) {
	// mantém o computador acordado e a tela acesa durante o espetáculo
	const esContinuous, esSystem, esDisplay = 0x80000000, 0x1, 0x2
	procSetExecutionState.Call(esContinuous | esSystem | esDisplay)
	defer procSetExecutionState.Call(esContinuous)

	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--autoplay-policy=no-user-gesture-required --disable-background-timer-throttling --disable-renderer-backgrounding --disable-backgrounding-occluded-windows")
	dataPath := ""
	if c, err := os.UserCacheDir(); err == nil {
		dataPath = filepath.Join(c, "Deixa", "WebView2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  "Deixa — Mesa de áudio",
			Width:  1440,
			Height: 900,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		log.Println("WebView2 indisponível; abrindo no navegador")
		openBrowser(url)
		message("O Deixa abriu no seu navegador porque o componente Microsoft Edge WebView2 não foi encontrado.\n\n"+
			"Para usar a janela própria do Deixa, instale o \"WebView2 Runtime\" (gratuito, da Microsoft).\n\n"+
			"Mantenha esta mensagem aberta durante o uso. Clique em OK para encerrar o Deixa.", false)
		return
	}
	defer w.Destroy()
	w.SetSize(1024, 640, webview2.HintMin)
	uiMu.Lock()
	uiView = w
	uiMu.Unlock()
	pending.mu.Lock()
	pending.onNew = func() {
		w.Dispatch(func() { w.Eval("window.checkPending && window.checkPending()") })
	}
	pending.mu.Unlock()
	w.Navigate(url)
	w.Run()
	uiMu.Lock()
	uiView = nil
	uiMu.Unlock()
}

func message(text string, isError bool) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Deixa")
	var flags uint32 = 0x40 // MB_ICONINFORMATION
	if isError {
		flags = 0x10 // MB_ICONERROR
	}
	windows.MessageBox(0, t, c, flags|0x10000 /* MB_SETFOREGROUND */)
	_ = unsafe.Pointer(nil)
}

func fatal(text string) {
	log.Println(text)
	message(text, true)
	os.Exit(1)
}

func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openFolder(dir string) {
	exec.Command("explorer", dir).Start()
}
