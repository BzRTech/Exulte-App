package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Pedidos de "Abrir com" que chegaram (pela linha de comando ou de uma
// segunda instância) e ainda não foram importados pela interface.
type pendingQueue struct {
	mu    sync.Mutex
	items []pendingItem
	onNew func()
}

type pendingItem struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (q *pendingQueue) add(paths []string) int {
	q.mu.Lock()
	n := 0
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		q.items = append(q.items, pendingItem{Path: p, Name: filepath.Base(p)})
		n++
	}
	cb := q.onNew
	q.mu.Unlock()
	if n > 0 && cb != nil {
		cb()
	}
	return n
}

func (q *pendingQueue) take() []pendingItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	if out == nil {
		out = []pendingItem{}
	}
	return out
}

type server struct {
	store   *Store
	conv    *Converter
	pending *pendingQueue
	dataDir string
}

func newServer(store *Store, conv *Converter, pending *pendingQueue, dataDir string, web fs.FS) http.Handler {
	s := &server{store: store, conv: conv, pending: pending, dataDir: dataDir}
	mux := http.NewServeMux()
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/project", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.Project())
	})
	mux.HandleFunc("PUT /api/project", func(w http.ResponseWriter, r *http.Request) {
		var p Project
		if err := json.NewDecoder(io.LimitReader(r.Body, 50<<20)).Decode(&p); err != nil {
			httpError(w, "projeto inválido", http.StatusBadRequest)
			return
		}
		if err := store.Save(p); err != nil {
			log.Println("salvar:", err)
			httpError(w, "não foi possível salvar: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/audio", s.importUpload)
	mux.HandleFunc("POST /api/import-path", s.importPath)
	mux.HandleFunc("POST /api/convert", s.convertStored)
	mux.HandleFunc("POST /api/discard", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			File string `json:"file"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
		store.RemoveUnused(req.File)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/pending", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, pending.take())
	})
	mux.HandleFunc("POST /api/pending", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			httpError(w, "pedido inválido", http.StatusBadRequest)
			return
		}
		n := pending.add(req.Paths)
		bringToFront()
		writeJSON(w, map[string]int{"queued": n})
	})
	mux.HandleFunc("GET /audio/{name}", func(w http.ResponseWriter, r *http.Request) {
		fp, ok := store.AudioPath(r.PathValue("name"))
		if !ok {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		f, err := os.Open(fp)
		if err != nil {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", contentTypeFor(fp))
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		p := store.Project()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFileName(p.Name)+`.zip"`)
		if err := store.Export(w, r.URL.Query().Get("media") != "0"); err != nil {
			log.Println("exportar:", err)
		}
	})
	// Salva o backup direto numa pasta (Documentos\Deixa - Backups) e abre o Explorer nele.
	mux.HandleFunc("POST /api/backup", func(w http.ResponseWriter, r *http.Request) {
		withMedia := r.URL.Query().Get("media") != "0"
		dir := backupDir(dataDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			httpError(w, "não foi possível criar a pasta de backups: "+err.Error(), http.StatusInternalServerError)
			return
		}
		p := store.Project()
		suffix := "com-midia"
		if !withMedia {
			suffix = "sem-midia"
		}
		name := fmt.Sprintf("%s %s %s.zip", zipSafe(p.Name), time.Now().Format("2006-01-02 15h04"), suffix)
		dst := filepath.Join(dir, name)
		tmp := dst + ".parcial"
		f, err := os.Create(tmp)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		err = store.Export(f, withMedia)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp, dst)
		}
		if err != nil {
			os.Remove(tmp)
			log.Println("backup:", err)
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		selectFile(dst)
		writeJSON(w, map[string]any{"path": dst})
	})
	mux.HandleFunc("POST /api/restore", func(w http.ResponseWriter, r *http.Request) {
		tmp, err := os.CreateTemp("", "restaurar-*")
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.CopyBuffer(tmp, r.Body, make([]byte, 1<<20))
		tmp.Close()
		if err != nil {
			httpError(w, "falha ao receber o arquivo", http.StatusBadRequest)
			return
		}
		p, err := store.Restore(tmp.Name())
		if err != nil {
			log.Println("importar:", err)
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Deixa-Missing", fmt.Sprint(store.Missing))
		writeJSON(w, p)
	})
	mux.HandleFunc("POST /api/open-folder", func(w http.ResponseWriter, r *http.Request) {
		openFolder(dataDir)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"dataDir": dataDir, "ffmpeg": conv.ffmpeg != "", "version": appVersion})
	})
	return guard(mux)
}

// guard aceita apenas pedidos locais; escritas exigem o cabeçalho X-Deixa
// (impede que páginas da internet mexam no projeto).
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") {
			http.Error(w, "acesso negado", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Deixa") != "1" {
			http.Error(w, "acesso negado", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type importResult struct {
	File      string `json:"file"`
	Converted bool   `json:"converted"`
	Native    bool   `json:"native"`
	Format    string `json:"format"`
}

// importUpload recebe o conteúdo do arquivo no corpo do pedido.
func (s *server) importUpload(w http.ResponseWriter, r *http.Request) {
	ext := r.URL.Query().Get("ext")
	tmp, err := os.CreateTemp(s.store.audio, ".entrada-*")
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.CopyBuffer(tmp, r.Body, make([]byte, 1<<20))
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err != nil {
		httpError(w, "falha ao receber o arquivo", http.StatusBadRequest)
		return
	}
	s.finishImport(w, tmp.Name(), ext, r.URL.Query().Get("name"))
}

// importPath importa um arquivo do disco (usado pelo "Abrir com" do Windows).
func (s *server) importPath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Path == "" {
		httpError(w, "pedido inválido", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(req.Path)
	if err != nil || st.IsDir() {
		httpError(w, "arquivo não encontrado", http.StatusNotFound)
		return
	}
	s.finishImport(w, req.Path, filepath.Ext(req.Path), filepath.Base(req.Path))
}

func (s *server) finishImport(w http.ResponseWriter, src, ext, display string) {
	res, err := s.importFile(src, ext, display, false)
	if err != nil {
		log.Println("importar áudio:", err)
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, res)
}

// importFile identifica o formato pelos bytes e copia (ou converte) para audios/.
// force=true converte mesmo formatos "nativos" (usado quando o player recusou o arquivo).
func (s *server) importFile(src, ext, display string, force bool) (importResult, error) {
	if display == "" {
		display = filepath.Base(src)
	}
	head := make([]byte, 4096)
	f, err := os.Open(src)
	if err != nil {
		return importResult{}, err
	}
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n == 0 {
		f.Close()
		return importResult{}, errors.New("arquivo vazio")
	}
	p := sniff(head, ext)
	if p.kind == kindNotAudio {
		f.Close()
		return importResult{}, errNotAudio
	}
	if !force && (p.kind == kindNative || p.kind == kindMaybe) {
		f.Seek(0, io.SeekStart)
		name, err := s.store.ImportAudio(f, p.ext)
		f.Close()
		return importResult{File: name, Native: true, Format: p.label}, err
	}
	f.Close()
	// a conversão precisa do arquivo com a extensão certa (o Windows decide pelo nome)
	// e com nome só ASCII: o Media Foundation trata o caminho como URL (acentos, # e % atrapalham)
	ext = p.ext
	if ext == "" || ext == "bin" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(src), "."))
	}
	if !fileNameRe.MatchString("x." + ext) {
		ext = "bin"
	}
	work := filepath.Join(s.store.audio, ".conv-"+newID()+"."+ext)
	if err := linkOrCopy(src, work); err != nil {
		return importResult{}, err
	}
	defer os.Remove(work)
	name := newID() + ".wav"
	dst := filepath.Join(s.store.audio, name)
	start := time.Now()
	if err := s.conv.ToWav(work, dst, p, display); err != nil {
		return importResult{}, err
	}
	log.Printf("convertido %s (%s) em %s", display, p.label, time.Since(start).Round(time.Millisecond))
	return importResult{File: name, Converted: true, Format: p.label}, nil
}

// convertStored converte um áudio já importado que o player não conseguiu abrir.
func (s *server) convertStored(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	fp, ok := s.store.AudioPath(req.File)
	if !ok {
		httpError(w, "arquivo inválido", http.StatusBadRequest)
		return
	}
	res, err := s.importFile(fp, filepath.Ext(fp), req.File, true)
	if err != nil {
		log.Println("converter:", err)
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.store.RemoveUnused(req.File)
	writeJSON(w, res)
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg; codecs=opus"
	case ".m4a", ".aac":
		if strings.EqualFold(filepath.Ext(name), ".aac") {
			return "audio/aac"
		}
		return "audio/mp4"
	case ".mp4":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".webm", ".weba":
		return "audio/webm"
	}
	return "application/octet-stream"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var fileNameReplacer = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ü", "u", "ç", "c")

func safeFileName(name string) string {
	s := fileNameReplacer.Replace(strings.ToLower(strings.TrimSpace(name)))
	s = strings.Map(func(r rune) rune {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		}
		return -1
	}, s)
	s = strings.Trim(s, "-")
	if s == "" {
		s = "espetaculo"
	}
	return s + "-" + time.Now().Format("2006-01-02")
}

// backupDir: Documentos\Deixa - Backups (ou, se não houver, Deixa-dados\backups).
func backupDir(dataDir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		for _, docs := range []string{"Documents", "Documentos"} {
			d := filepath.Join(home, docs)
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				return filepath.Join(d, "Deixa - Backups")
			}
		}
	}
	return filepath.Join(dataDir, "backups")
}
