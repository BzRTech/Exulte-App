//go:build !windows

package main

// Versão sem janela, usada para testes fora do Windows: serve a interface
// e espera Ctrl+C.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
)

func acquireSingleInstance() (func(), bool) { return func() {}, true }
func allowForeground()                      {}
func bringToFront()                         {}

func runUI(url string, pending *pendingQueue) {
	fmt.Println("DEIXA_URL", url)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

func message(text string, isError bool) { fmt.Fprintln(os.Stderr, text) }

func fatal(text string) {
	log.Println(text)
	fmt.Fprintln(os.Stderr, text)
	os.Exit(1)
}

func openBrowser(url string) { exec.Command("xdg-open", url).Start() }
func openFolder(dir string)  { exec.Command("xdg-open", dir).Start() }

func selectFile(path string) { openFolder(filepath.Dir(path)) }
