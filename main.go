// Deixa — mesa de áudio para espetáculos, empacotada como aplicativo de desktop.
//
// O executável sobe um servidor HTTP somente em 127.0.0.1 (porta aleatória),
// serve a interface embutida e guarda o projeto e os áudios importados em uma
// pasta no disco. No Windows a interface abre numa janela WebView2 (o motor do
// Edge que já vem no sistema); nos demais sistemas abre no navegador padrão.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed web
var webFS embed.FS

//go:embed sons/*.wav
var sonsFS embed.FS

const appName = "Deixa"

func main() {
	dir, err := dataDir()
	if err != nil {
		fatal("Não foi possível criar a pasta de dados do Deixa.\n\n" + err.Error())
	}
	logFile, _ := os.OpenFile(filepath.Join(dir, "deixa.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if logFile != nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	store, err := openStore(dir)
	if err != nil {
		fatal("Não foi possível abrir o projeto salvo.\n\n" + err.Error())
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("Não foi possível iniciar o Deixa.\n\n" + err.Error())
	}
	ui, _ := fs.Sub(webFS, "web")
	srv := &http.Server{Handler: newServer(store, ui), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("servidor:", err)
		}
	}()
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	log.Println("Deixa em", url, "dados em", dir)

	runUI(url, dir) // bloqueia até a janela ser fechada

	// dá tempo para o último salvamento terminar antes de sair
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// dataDir usa uma pasta "Deixa-dados" ao lado do executável (modo portátil:
// basta copiar a pasta junto com o .exe para levar o espetáculo para outro
// computador). Se ali não for possível gravar, usa a pasta do usuário.
func dataDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		d := filepath.Join(filepath.Dir(exe), appName+"-dados")
		if writable(d) {
			return d, nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, appName)
	return d, os.MkdirAll(d, 0o755)
}

func writable(d string) bool {
	if err := os.MkdirAll(d, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(d, ".teste-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
