//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	procShowWindow              = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
)

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

func runUI(url, dataDir string) {
	// Só uma janela por vez, para dois Deixa não gravarem o mesmo projeto.
	name, _ := windows.UTF16PtrFromString(`Local\DeixaMesaDeAudio`)
	if _, err := windows.CreateMutex(nil, true, name); err == windows.ERROR_ALREADY_EXISTS {
		message("O Deixa já está aberto em outra janela.", windows.MB_ICONINFORMATION)
		return
	}

	// Durante o espetáculo o computador não pode dormir nem apagar a tela.
	procSetThreadExecutionState.Call(esContinuous | esSystemRequired | esDisplayRequired)
	defer procSetThreadExecutionState.Call(esContinuous)

	// Áudio sem bloqueio de reprodução automática (avanço automático de faixa)
	// e sem desaceleração quando a janela fica em segundo plano/minimizada.
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--autoplay-policy=no-user-gesture-required --disable-background-timer-throttling "+
			"--disable-renderer-backgrounding --disable-backgrounding-occluded-windows")

	cache, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		cache = dataDir
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(cache, "Deixa", "WebView2"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Deixa — Mesa de áudio",
			Width:  1440,
			Height: 900,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		// Sem o runtime WebView2 (Windows muito antigo): usa o navegador padrão.
		openBrowser(url)
		message("O Deixa abriu no seu navegador porque o componente Microsoft Edge WebView2 não foi encontrado.\n\n"+
			"Para usar a janela própria do Deixa, instale o \"WebView2 Runtime\" (gratuito, da Microsoft).\n\n"+
			"Mantenha esta mensagem aberta durante o uso. Clique em OK para encerrar o Deixa.", windows.MB_ICONWARNING)
		return
	}
	defer w.Destroy()
	w.SetSize(1024, 640, webview2.HintMin)
	procShowWindow.Call(uintptr(w.Window()), 3) // SW_MAXIMIZE: abre em tela cheia (com barra de título)
	w.Navigate(url)
	w.Run()
}

func message(text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Deixa")
	windows.MessageBox(0, t, c, flags|windows.MB_OK|windows.MB_SETFOREGROUND)
}

func fatal(text string) {
	message(text, windows.MB_ICONERROR)
	os.Exit(1)
}

func openBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func openFolder(dir string) {
	_ = exec.Command("explorer", dir).Start()
}
