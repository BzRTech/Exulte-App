package main

import (
	"archive/zip"
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const projectFormat = "deixa-desktop-1"

type Cue struct {
	ID       string  `json:"id"`
	Time     float64 `json:"time"`
	Action   string  `json:"action"` // "effect" ou "pause"
	EffectID *string `json:"effectId"`
}

type Track struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Cues     []Cue   `json:"cues"`
}

type Effect struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Builtin  bool    `json:"builtin,omitempty"`
}

type Project struct {
	Format   string   `json:"format"`
	Name     string   `json:"name"`
	Tracks   []Track  `json:"tracks"`
	Effects  []Effect `json:"effects"`
	Auto     bool     `json:"auto"`
	MusicVol float64  `json:"musicVol"`
	FxVol    float64  `json:"fxVol"`
}

// Store guarda o projeto (projeto.json) e os áudios (pasta audios/).
type Store struct {
	dir, audioDir string
	mu            sync.Mutex
	project       Project
}

var fileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,80}\.[a-z0-9]{1,5}$`)

var audioExts = map[string]bool{"mp3": true, "wav": true, "ogg": true, "oga": true, "opus": true, "m4a": true,
	"mp4": true, "aac": true, "flac": true, "webm": true, "weba": true, "bin": true}

func openStore(dir string) (*Store, error) {
	s := &Store{dir: dir, audioDir: filepath.Join(dir, "audios")}
	if err := os.MkdirAll(s.audioDir, 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.projectPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p, err := s.defaultProject()
		if err != nil {
			return nil, err
		}
		if err := s.Save(p); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		var p Project
		if err := json.Unmarshal(raw, &p); err == nil {
			err = s.validate(&p)
		}
		if err != nil {
			// projeto danificado: guarda uma cópia e tenta a versão anterior
			bad := filepath.Join(dir, "projeto-danificado-"+time.Now().Format("20060102-150405")+".json")
			_ = os.Rename(s.projectPath(), bad)
			log.Println("projeto danificado, movido para", bad, err)
			if prev, e := os.ReadFile(s.projectPath() + ".anterior"); e == nil && json.Unmarshal(prev, &p) == nil && s.validate(&p) == nil {
				err = s.Save(p)
			} else if p, err = s.defaultProject(); err == nil {
				err = s.Save(p)
			}
			if err != nil {
				return nil, err
			}
		} else {
			s.project = p
		}
	}
	s.cleanup()
	return s, nil
}

func (s *Store) projectPath() string { return filepath.Join(s.dir, "projeto.json") }

func (s *Store) Project() Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.project
}

func (s *Store) validate(p *Project) error {
	if p.Format == "" {
		p.Format = projectFormat
	}
	if p.Format != projectFormat {
		return fmt.Errorf("formato desconhecido: %q", p.Format)
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		p.Name = "Meu espetáculo"
	}
	if p.Tracks == nil {
		p.Tracks = []Track{}
	}
	if p.Effects == nil {
		p.Effects = []Effect{}
	}
	p.MusicVol = clamp(p.MusicVol, 0, 100)
	p.FxVol = clamp(p.FxVol, 0, 100)
	for i := range p.Tracks {
		t := &p.Tracks[i]
		if t.ID == "" || !fileNameRe.MatchString(t.File) {
			return fmt.Errorf("faixa inválida: %q", t.Name)
		}
		if t.Cues == nil {
			t.Cues = []Cue{}
		}
		for _, c := range t.Cues {
			if c.ID == "" || (c.Action != "effect" && c.Action != "pause") {
				return fmt.Errorf("deixa inválida em %q", t.Name)
			}
		}
	}
	for _, e := range p.Effects {
		if e.ID == "" || !fileNameRe.MatchString(e.File) {
			return fmt.Errorf("efeito inválido: %q", e.Name)
		}
	}
	return nil
}

func clamp(v, lo, hi float64) float64 {
	if v != v || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Save grava o projeto de forma atômica, mantendo a versão anterior.
func (s *Store) Save(p Project) error {
	if err := s.validate(&p); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := s.projectPath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := os.Stat(s.projectPath()); err == nil {
		_ = copyFile(s.projectPath(), s.projectPath()+".anterior")
	}
	if err := os.Rename(tmp, s.projectPath()); err != nil {
		return err
	}
	s.project = p
	return nil
}

// ImportAudio grava o áudio recebido na pasta audios/ e devolve o nome do arquivo.
func (s *Store) ImportAudio(r io.Reader, ext string) (string, error) {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if !audioExts[ext] {
		ext = "bin"
	}
	name := newID() + "." + ext
	dst := filepath.Join(s.audioDir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, bufio.NewReaderSize(r, 1<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return "", err
	}
	return name, nil
}

func (s *Store) AudioPath(name string) (string, bool) {
	if !fileNameRe.MatchString(name) {
		return "", false
	}
	return filepath.Join(s.audioDir, name), true
}

// cleanup apaga áudios que não pertencem mais ao projeto (faixas excluídas).
// Roda só na abertura, quando não há importações em andamento.
func (s *Store) cleanup() {
	used := map[string]bool{}
	p := s.Project()
	for _, t := range p.Tracks {
		used[t.File] = true
	}
	for _, e := range p.Effects {
		used[e.File] = true
	}
	entries, _ := os.ReadDir(s.audioDir)
	for _, e := range entries {
		if !e.IsDir() && !used[e.Name()] {
			_ = os.Remove(filepath.Join(s.audioDir, e.Name()))
		}
	}
}

// ---------------------------------------------------------------- efeitos padrão

var builtinEffects = []struct{ file, name string }{
	{"campainha-ding-dong.wav", "Campainha"},
	{"campainha-eletrica.wav", "Campainha elétrica"},
	{"freio-de-carro.wav", "Freio de carro"},
	{"freio-de-carro-longo.wav", "Freada longa"},
}

func (s *Store) builtinEffects() ([]Effect, error) {
	var out []Effect
	for _, b := range builtinEffects {
		data, err := sonsFS.ReadFile("sons/" + b.file)
		if err != nil {
			return nil, err
		}
		name := "padrao-" + b.file
		if err := os.WriteFile(filepath.Join(s.audioDir, name), data, 0o644); err != nil {
			return nil, err
		}
		out = append(out, Effect{ID: "padrao-" + strings.TrimSuffix(b.file, ".wav"), Name: b.name, File: name,
			Duration: wavDuration(data), Builtin: true})
	}
	return out, nil
}

func (s *Store) defaultProject() (Project, error) {
	fx, err := s.builtinEffects()
	if err != nil {
		return Project{}, err
	}
	return Project{Format: projectFormat, Name: "Meu espetáculo", Tracks: []Track{}, Effects: fx,
		MusicVol: 80, FxVol: 80}, nil
}

func wavDuration(b []byte) float64 {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0
	}
	var byteRate uint32
	for i := 12; i+8 <= len(b); {
		id, size := string(b[i:i+4]), binary.LittleEndian.Uint32(b[i+4:i+8])
		switch id {
		case "fmt ":
			if i+20 <= len(b) {
				byteRate = binary.LittleEndian.Uint32(b[i+16 : i+20])
			}
		case "data":
			if byteRate > 0 {
				return float64(size) / float64(byteRate)
			}
		}
		i += 8 + int(size) + int(size&1)
	}
	return 0
}

// ---------------------------------------------------------------- backup

// Export escreve um .zip com o projeto e todos os áudios usados.
func (s *Store) Export(w io.Writer) error {
	p := s.Project()
	zw := zip.NewWriter(w)
	meta, _ := json.MarshalIndent(p, "", " ")
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: "projeto.json", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	if _, err := fw.Write(meta); err != nil {
		return err
	}
	seen := map[string]bool{}
	files := []string{}
	for _, t := range p.Tracks {
		files = append(files, t.File)
	}
	for _, e := range p.Effects {
		files = append(files, e.File)
	}
	for _, name := range files {
		if seen[name] {
			continue
		}
		seen[name] = true
		f, err := os.Open(filepath.Join(s.audioDir, name))
		if err != nil {
			return err
		}
		// áudio já é comprimido: armazenar sem recomprimir é muito mais rápido
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: "audios/" + name, Method: zip.Store, Modified: time.Now()})
		if err == nil {
			_, err = io.Copy(fw, f)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// Restore substitui o projeto atual por um backup (.zip deste aplicativo ou
// .json exportado pela versão de navegador do Deixa).
func (s *Store) Restore(src string) (Project, error) {
	head := make([]byte, 4)
	f, err := os.Open(src)
	if err != nil {
		return Project{}, err
	}
	_, _ = io.ReadFull(f, head)
	f.Close()
	var p Project
	if string(head[:2]) == "PK" {
		p, err = s.restoreZip(src)
	} else {
		p, err = s.restoreLegacyJSON(src)
	}
	if err != nil {
		return Project{}, err
	}
	return p, s.Save(p)
}

func (s *Store) restoreZip(src string) (Project, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return Project{}, errors.New("arquivo .zip inválido")
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	mf, ok := byName["projeto.json"]
	if !ok {
		return Project{}, errors.New("o .zip não contém projeto.json")
	}
	rc, err := mf.Open()
	if err != nil {
		return Project{}, err
	}
	var p Project
	err = json.NewDecoder(io.LimitReader(rc, 50<<20)).Decode(&p)
	rc.Close()
	if err != nil {
		return Project{}, errors.New("projeto.json inválido")
	}
	if err := s.validate(&p); err != nil {
		return Project{}, err
	}
	renamed := map[string]string{}
	extract := func(name string) (string, error) {
		if n, ok := renamed[name]; ok {
			return n, nil
		}
		zf, ok := byName[path.Join("audios", name)]
		if !ok {
			return "", fmt.Errorf("áudio ausente no backup: %s", name)
		}
		rc, err := zf.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		n, err := s.ImportAudio(rc, path.Ext(name))
		renamed[name] = n
		return n, err
	}
	for i := range p.Tracks {
		if p.Tracks[i].File, err = extract(p.Tracks[i].File); err != nil {
			return Project{}, err
		}
	}
	for i := range p.Effects {
		if p.Effects[i].File, err = extract(p.Effects[i].File); err != nil {
			return Project{}, err
		}
	}
	return p, nil
}

type legacyAsset struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Duration float64 `json:"duration"`
	Demo     bool    `json:"demo"`
	Data     string  `json:"data"`
	Cues     []Cue   `json:"cues"`
}

type legacyBackup struct {
	Format  string `json:"format"`
	Project struct {
		Name     string        `json:"name"`
		Tracks   []legacyAsset `json:"tracks"`
		Effects  []legacyAsset `json:"effects"`
		Auto     bool          `json:"auto"`
		MusicVol float64       `json:"musicVol"`
		FxVol    float64       `json:"fxVol"`
	} `json:"project"`
}

var mimeExt = map[string]string{"audio/mpeg": "mp3", "audio/mp3": "mp3", "audio/wav": "wav", "audio/x-wav": "wav",
	"audio/wave": "wav", "audio/vnd.wave": "wav", "audio/ogg": "ogg", "audio/opus": "opus", "audio/mp4": "m4a",
	"audio/x-m4a": "m4a", "audio/aac": "aac", "audio/flac": "flac", "audio/x-flac": "flac", "audio/webm": "webm"}

func (s *Store) restoreLegacyJSON(src string) (Project, error) {
	f, err := os.Open(src)
	if err != nil {
		return Project{}, err
	}
	defer f.Close()
	var b legacyBackup
	if err := json.NewDecoder(bufio.NewReaderSize(f, 1<<20)).Decode(&b); err != nil || b.Format != "deixa-1" {
		return Project{}, errors.New("selecione um backup .zip do Deixa ou um .json exportado pela versão de navegador")
	}
	decode := func(a legacyAsset) (string, error) {
		comma := strings.IndexByte(a.Data, ',')
		if !strings.HasPrefix(a.Data, "data:") || comma < 0 {
			return "", fmt.Errorf("áudio inválido em %q", a.Name)
		}
		mime := strings.SplitN(a.Data[5:comma], ";", 2)[0]
		ext := mimeExt[strings.ToLower(mime)]
		r := base64.NewDecoder(base64.StdEncoding, strings.NewReader(a.Data[comma+1:]))
		return s.ImportAudio(r, ext)
	}
	p := Project{Format: projectFormat, Name: b.Project.Name, Auto: b.Project.Auto,
		MusicVol: b.Project.MusicVol, FxVol: b.Project.FxVol, Tracks: []Track{}, Effects: []Effect{}}
	kept := map[string]bool{}
	for _, a := range b.Project.Effects {
		if a.Demo {
			continue // sons de demonstração sintetizados da versão de navegador
		}
		file, err := decode(a)
		if err != nil {
			return Project{}, err
		}
		kept[a.ID] = true
		p.Effects = append(p.Effects, Effect{ID: a.ID, Name: a.Name, File: file, Duration: a.Duration})
	}
	builtins, err := s.builtinEffects()
	if err != nil {
		return Project{}, err
	}
	p.Effects = append(p.Effects, builtins...)
	for _, a := range b.Project.Tracks {
		if a.Demo {
			continue
		}
		file, err := decode(a)
		if err != nil {
			return Project{}, err
		}
		cues := []Cue{}
		for _, c := range a.Cues {
			if c.Action == "pause" || (c.EffectID != nil && kept[*c.EffectID]) {
				cues = append(cues, c)
			}
		}
		p.Tracks = append(p.Tracks, Track{ID: a.ID, Name: a.Name, File: file, Duration: a.Duration, Cues: cues})
	}
	return p, nil
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
