package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func newServer(store *Store, ui fs.FS) http.Handler {
	mux := http.NewServeMux()
	static := http.FileServer(http.FS(ui))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /api/project", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.Project())
	})
	saveProject := func(w http.ResponseWriter, r *http.Request) {
		var p Project
		if err := json.NewDecoder(io.LimitReader(r.Body, 50<<20)).Decode(&p); err != nil {
			httpError(w, "projeto inválido", http.StatusBadRequest)
			return
		}
		if err := store.Save(p); err != nil {
			log.Println("salvar:", err)
			httpError(w, "não foi possível salvar: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /api/project", saveProject)

	mux.HandleFunc("POST /api/audio", func(w http.ResponseWriter, r *http.Request) {
		name, err := store.ImportAudio(r.Body, r.URL.Query().Get("ext"))
		if err != nil {
			log.Println("importar:", err)
			httpError(w, "não foi possível gravar o áudio: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"file": name})
	})

	mux.HandleFunc("GET /audio/{name}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := store.AudioPath(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// nomes são únicos e o conteúdo nunca muda: pode ficar em cache
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeContent(w, r, st.Name(), st.ModTime(), f) // suporta Range (busca na música)
	})

	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		name := safeFileName(store.Project().Name)
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
		if err := store.Export(w); err != nil {
			log.Println("exportar:", err)
		}
	})

	mux.HandleFunc("POST /api/restore", func(w http.ResponseWriter, r *http.Request) {
		tmp, err := os.CreateTemp(store.dir, "restaurar-*")
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, r.Body)
		tmp.Close()
		if err != nil {
			httpError(w, "falha ao receber o arquivo", http.StatusBadRequest)
			return
		}
		p, err := store.Restore(tmp.Name())
		if err != nil {
			log.Println("restaurar:", err)
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, p)
	})

	mux.HandleFunc("POST /api/open-folder", func(w http.ResponseWriter, r *http.Request) {
		openFolder(store.dir)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"dataDir": store.dir})
	})

	return guard(mux)
}

// guard aceita apenas requisições locais endereçadas ao próprio Deixa
// (protege contra DNS rebinding) e exige um cabeçalho próprio nas operações
// que alteram dados (páginas de outros sites não conseguem enviá-lo).
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var accents = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "ê", "e", "è", "e",
	"í", "i", "ì", "i", "ó", "o", "ô", "o", "õ", "o", "ò", "o", "ú", "u", "ü", "u", "ç", "c", "ñ", "n",
	"Á", "A", "À", "A", "Â", "A", "Ã", "A", "É", "E", "Ê", "E", "Í", "I", "Ó", "O", "Ô", "O", "Õ", "O", "Ú", "U", "Ç", "C")

// safeFileName gera um nome de arquivo ASCII (compatível com qualquer navegador/Windows).
func safeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r > 126 || strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '-'
		}
		return r
	}, accents.Replace(strings.TrimSpace(s)))
	if s == "" {
		s = "espetaculo"
	}
	return "deixa-" + s + "-" + time.Now().Format("2006-01-02")
}
