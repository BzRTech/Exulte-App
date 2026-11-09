package main

// Deixa — mesa de áudio para espetáculos (versão desktop).
// Um servidor HTTP local (127.0.0.1) entrega a interface e os áudios para
// uma janela WebView2. Projeto e áudios ficam na pasta Deixa-dados.

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const appVersion = "2.1.0"

//go:embed web
var webFiles embed.FS

func main() {
	dir, err := dataDir()
	if err != nil {
		fatal("Não foi possível criar a pasta de dados: " + err.Error())
	}
	if lf, err := os.OpenFile(filepath.Join(dir, "deixa.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}
	paths := fileArgs(os.Args[1:])

	// Só uma janela por vez. Se já houver uma aberta, os arquivos vão para ela.
	release, first := acquireSingleInstance()
	if !first {
		if forwardToRunning(dir, paths) {
			return
		}
		message("O Deixa já está aberto em outra janela.", false)
		return
	}
	defer release()

	store, err := openStore(dir)
	if err != nil {
		fatal("Não foi possível abrir o projeto: " + err.Error())
	}
	conv := newConverter(dir, log.Printf)
	pending := &pendingQueue{}
	pending.add(paths)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("Não foi possível iniciar o servidor local: " + err.Error())
	}
	os.WriteFile(filepath.Join(dir, ".porta"), []byte(ln.Addr().String()), 0o644)
	defer os.Remove(filepath.Join(dir, ".porta"))

	web, _ := fs.Sub(webFiles, "web")
	srv := &http.Server{Handler: newServer(store, conv, pending, dir, web), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("servidor:", err)
		}
	}()
	url := fmt.Sprintf("http://%s/", ln.Addr())
	log.Println("Deixa", appVersion, "em", url, "dados em", dir, "ffmpeg:", conv.ffmpeg)

	runUI(url, pending)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// fileArgs ignora opções (-x, /x) e mantém caminhos de arquivos.
func fileArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || (len(a) == 2 && a[0] == '/') {
			continue
		}
		out = append(out, a)
	}
	return out
}

// dataDir usa Deixa-dados ao lado do programa (modo portátil) ou, se essa
// pasta não puder ser gravada, %APPDATA%\Deixa-dados.
func dataDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		d := filepath.Join(filepath.Dir(exe), "Deixa-dados")
		if writable(d) {
			return d, nil
		}
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(cfg, "Deixa-dados")
	return d, os.MkdirAll(d, 0o755)
}

func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".teste-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// forwardToRunning entrega os arquivos à janela já aberta. Tenta por alguns
// segundos, pois a outra instância pode estar acabando de iniciar (o Windows
// abre uma instância por arquivo quando vários são abertos de uma vez).
func forwardToRunning(dir string, paths []string) bool {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		if a, err := filepath.Abs(p); err == nil {
			p = a
		}
		abs = append(abs, p)
	}
	body, _ := json.Marshal(map[string][]string{"paths": abs})
	allowForeground()
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		addr, err := os.ReadFile(filepath.Join(dir, ".porta"))
		if err == nil && len(addr) > 0 {
			req, _ := http.NewRequest("POST", "http://"+strings.TrimSpace(string(addr))+"/api/pending", bytes.NewReader(body))
			req.Header.Set("X-Deixa", "1")
			req.Header.Set("Content-Type", "application/json")
			if res, err := client.Do(req); err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return true
				}
				if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
					return false // versão antiga aberta: não sabe receber arquivos
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}
