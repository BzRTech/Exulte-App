//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
)

// Fora do Windows (desenvolvimento/testes) a interface abre no navegador.
func runUI(url, dataDir string) {
	fmt.Println("Deixa rodando em", url)
	fmt.Println("Dados em", dataDir)
	fmt.Println("Ctrl+C para encerrar.")
	if os.Getenv("DEIXA_NO_BROWSER") == "" {
		openBrowser(url)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}

func fatal(text string) {
	fmt.Fprintln(os.Stderr, text)
	os.Exit(1)
}

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	_ = exec.Command(cmd, url).Start()
}

func openFolder(dir string) { openBrowser(dir) }
