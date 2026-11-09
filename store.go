package main

// Armazenamento do espetáculo: projeto.json + pasta audios/, dentro da pasta
// de dados (Deixa-dados). Formato idêntico ao da versão 2.0 do Deixa, para
// que espetáculos já montados continuem abrindo sem nenhuma migração.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const projectFormat = "deixa-desktop-1"

//go:embed sons
var builtinSounds embed.FS

type Cue struct {
	ID       string  `json:"id"`
	Time     float64 `json:"time"`
	Action   string  `json:"action"`
	EffectID *string `json:"effectId"`
}

type Track struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Cues     []Cue   `json:"cues"`
	Scene    string  `json:"scene,omitempty"` // trecho do roteiro ligado a esta música
	Notes    string  `json:"notes,omitempty"` // anotações do operador para esta música/cena
}

type Effect struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Builtin  bool    `json:"builtin,omitempty"`
	Vol      float64 `json:"vol,omitempty"` // volume próprio do efeito, em % (vazio = 100)
}

type Project struct {
	Format   string   `json:"format"`
	Name     string   `json:"name"`
	Tracks   []Track  `json:"tracks"`
	Effects  []Effect `json:"effects"`
	Auto     bool     `json:"auto"`
	MusicVol float64  `json:"musicVol"`
	FxVol    float64  `json:"fxVol"`
	Eq       *EQ      `json:"eq,omitempty"`
	Script   string   `json:"script,omitempty"`
	ScriptFs float64  `json:"scriptFs,omitempty"`
	// anotações do operador por cena (chave: título da cena no roteiro)
	SceneNotes map[string]string `json:"sceneNotes,omitempty"`
}

// EQ é o equalizador geral (músicas e efeitos): ganho em dB de cada faixa.
type EQ struct {
	On    bool      `json:"on"`
	Gains []float64 `json:"gains"`
}

var fileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,80}\.[a-z0-9]{1,5}$`)

// extensões que o servidor guarda com o próprio nome; o resto vira .bin
// (o conteúdo é identificado pelos bytes, não pela extensão).
var knownExt = map[string]bool{
	"mp3": true, "wav": true, "ogg": true, "oga": true, "opus": true, "m4a": true,
	"mp4": true, "aac": true, "flac": true, "webm": true, "weba": true,
	// formatos que passam por conversão
	"wma": true, "wmv": true, "asf": true, "aif": true, "aiff": true, "aifc": true,
	"au": true, "snd": true, "amr": true, "3gp": true, "3g2": true, "ac3": true,
	"eac3": true, "ec3": true, "caf": true, "mov": true, "avi": true, "mkv": true,
	"mka": true, "mp2": true, "mpa": true, "m4b": true, "m4r": true, "alac": true,
	"ape": true, "wv": true, "tta": true, "dts": true, "spx": true, "mpg": true,
	"mpeg": true, "vob": true, "ts": true, "mts": true, "m2ts": true, "flv": true,
	"rm": true, "ra": true, "voc": true, "w64": true, "rf64": true, "bwf": true,
	"mid": false,
}

type Store struct {
	dir    string
	audio  string
	mu     sync.Mutex
	proj   Project
	saveMu sync.Mutex
}

func openStore(dir string) (*Store, error) {
	s := &Store{dir: dir, audio: filepath.Join(dir, "audios")}
	if err := os.MkdirAll(s.audio, 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "projeto.json"))
	switch {
	case err == nil:
		var p Project
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("projeto.json corrompido: %w", err)
		}
		if err := s.validate(&p); err != nil {
			return nil, err
		}
		s.proj = p
	case errors.Is(err, os.ErrNotExist):
		p, err := s.defaultProject()
		if err != nil {
			return nil, err
		}
		if err := s.Save(p); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	s.cleanup()
	return s, nil
}

func (s *Store) Project() Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proj
}

func (s *Store) validate(p *Project) error {
	if p.Format != "" && p.Format != projectFormat {
		return fmt.Errorf("formato desconhecido: %q", p.Format)
	}
	p.Format = projectFormat
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
	for i := range p.Tracks {
		t := &p.Tracks[i]
		if !fileNameRe.MatchString(t.File) {
			return fmt.Errorf("faixa inválida: %q", t.Name)
		}
		if t.Cues == nil {
			t.Cues = []Cue{}
		}
		for _, c := range t.Cues {
			if c.Action != "effect" && c.Action != "pause" {
				return fmt.Errorf("deixa inválida em %q", t.Name)
			}
		}
	}
	for _, e := range p.Effects {
		if !fileNameRe.MatchString(e.File) {
			return fmt.Errorf("efeito inválido: %q", e.Name)
		}
	}
	p.MusicVol = clamp(p.MusicVol, 0, 100)
	for i := range p.Effects {
		if p.Effects[i].Vol != 0 {
			p.Effects[i].Vol = clamp(p.Effects[i].Vol, 1, 300)
		}
	}
	if len(p.Script) > 2<<20 {
		p.Script = p.Script[:2<<20]
	}
	for k, v := range p.SceneNotes {
		if len(p.SceneNotes) > 2000 || len(k) > 500 || len(v) > 20000 {
			delete(p.SceneNotes, k)
		}
	}
	if p.ScriptFs != 0 {
		p.ScriptFs = clamp(p.ScriptFs, 12, 40)
	}
	if p.Eq != nil {
		if len(p.Eq.Gains) > 10 {
			p.Eq.Gains = p.Eq.Gains[:10]
		}
		for i, g := range p.Eq.Gains {
			p.Eq.Gains[i] = clamp(g, -12, 12)
		}
	}
	p.FxVol = clamp(p.FxVol, 0, 100)
	return nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Save grava de forma atômica: arquivo temporário + fsync + rename, mantendo
// a versão anterior em projeto.json.anterior.
func (s *Store) Save(p Project) error {
	if err := s.validate(&p); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	final := filepath.Join(s.dir, "projeto.json")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := os.Stat(final); err == nil {
		copyFile(final, final+".anterior")
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.proj = p
	s.mu.Unlock()
	return nil
}

// ImportAudio copia um áudio para a pasta audios/ com um nome novo.
func (s *Store) ImportAudio(r io.Reader, ext string) (string, error) {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if !knownExt[ext] {
		ext = "bin"
	}
	name := newID() + "." + ext
	dst := filepath.Join(s.audio, name)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = io.CopyBuffer(f, r, make([]byte, 1<<20))
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
	return filepath.Join(s.audio, name), true
}

// Remove apaga um áudio que não está em uso por nenhuma faixa ou efeito.
func (s *Store) RemoveUnused(name string) {
	p := s.Project()
	for _, t := range p.Tracks {
		if t.File == name {
			return
		}
	}
	for _, e := range p.Effects {
		if e.File == name {
			return
		}
	}
	if fp, ok := s.AudioPath(name); ok {
		os.Remove(fp)
	}
}

// cleanup apaga, na abertura, áudios que não pertencem mais ao projeto.
func (s *Store) cleanup() {
	p := s.Project()
	used := map[string]bool{}
	for _, t := range p.Tracks {
		used[t.File] = true
	}
	for _, e := range p.Effects {
		used[e.File] = true
	}
	entries, err := os.ReadDir(s.audio)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || used[e.Name()] {
			continue
		}
		os.Remove(filepath.Join(s.audio, e.Name()))
	}
}

var builtinList = []struct{ file, name string }{
	{"campainha-ding-dong.wav", "Campainha"},
	{"campainha-eletrica.wav", "Campainha elétrica"},
	{"freio-de-carro.wav", "Freio de carro"},
	{"freio-de-carro-longo.wav", "Freada longa"},
}

func (s *Store) builtinEffects() ([]Effect, error) {
	var out []Effect
	for _, b := range builtinList {
		data, err := builtinSounds.ReadFile("sons/" + b.file)
		if err != nil {
			return nil, err
		}
		file := "padrao-" + b.file
		if err := os.WriteFile(filepath.Join(s.audio, file), data, 0o644); err != nil {
			return nil, err
		}
		dur := 0.0
		if strings.HasSuffix(b.file, ".wav") {
			dur = wavDuration(data)
		}
		out = append(out, Effect{ID: newID(), Name: b.name, File: file, Duration: dur, Builtin: true})
	}
	return out, nil
}

func (s *Store) defaultProject() (Project, error) {
	fx, err := s.builtinEffects()
	if err != nil {
		return Project{}, err
	}
	return Project{Format: projectFormat, Name: "Meu espetáculo", Tracks: []Track{}, Effects: fx, MusicVol: 80, FxVol: 80}, nil
}

func wavDuration(b []byte) float64 {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0
	}
	var byteRate uint32
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		body := i + 8
		switch id {
		case "fmt ":
			if body+12 <= len(b) {
				byteRate = binary.LittleEndian.Uint32(b[body+8 : body+12])
			}
		case "data":
			if byteRate == 0 {
				return 0
			}
			if body+size > len(b) {
				size = len(b) - body
			}
			return float64(size) / float64(byteRate)
		}
		i = body + size + size&1
	}
	return 0
}

// ------------------------------------------------------------------ backup

func (s *Store) Export(w io.Writer) error {
	p := s.Project()
	zw := zip.NewWriter(w)
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	now := time.Now()
	jw, err := zw.CreateHeader(&zip.FileHeader{Name: "projeto.json", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	if _, err := jw.Write(b); err != nil {
		return err
	}
	seen := map[string]bool{}
	var files []string
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
		f, err := os.Open(filepath.Join(s.audio, name))
		if err != nil {
			return fmt.Errorf("áudio ausente: %s", name)
		}
		aw, err := zw.CreateHeader(&zip.FileHeader{Name: "audios/" + name, Method: zip.Store, Modified: now})
		if err == nil {
			_, err = io.CopyBuffer(aw, f, make([]byte, 1<<20))
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// Restore abre um backup .zip (versão desktop) ou .json (versão de navegador).
func (s *Store) Restore(file string) (Project, error) {
	head := make([]byte, 4)
	f, err := os.Open(file)
	if err != nil {
		return Project{}, err
	}
	n, _ := io.ReadAtLeast(f, head, 2)
	f.Close()
	var p Project
	if n >= 2 && head[0] == 'P' && head[1] == 'K' {
		p, err = s.restoreZip(file)
	} else {
		p, err = s.restoreLegacyJSON(file)
	}
	if err != nil {
		return Project{}, err
	}
	if err := s.Save(p); err != nil {
		return Project{}, err
	}
	return s.Project(), nil
}

func (s *Store) restoreZip(file string) (Project, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return Project{}, fmt.Errorf("arquivo .zip inválido")
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	pf, ok := byName["projeto.json"]
	if !ok {
		return Project{}, fmt.Errorf("este .zip não é um backup do Deixa")
	}
	rc, err := pf.Open()
	if err != nil {
		return Project{}, err
	}
	var p Project
	err = json.NewDecoder(rc).Decode(&p)
	rc.Close()
	if err != nil {
		return Project{}, fmt.Errorf("projeto.json inválido no backup")
	}
	if err := s.validate(&p); err != nil {
		return Project{}, err
	}
	renamed := map[string]string{}
	importOne := func(name string) (string, error) {
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
		ext := ""
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			ext = name[i+1:]
		}
		n, err := s.ImportAudio(rc, ext)
		if err != nil {
			return "", err
		}
		renamed[name] = n
		return n, nil
	}
	for i := range p.Tracks {
		if p.Tracks[i].File, err = importOne(p.Tracks[i].File); err != nil {
			return Project{}, err
		}
	}
	for i := range p.Effects {
		if p.Effects[i].File, err = importOne(p.Effects[i].File); err != nil {
			return Project{}, err
		}
	}
	return p, nil
}

// --- backup .json da versão de navegador (áudios embutidos como data: URL)

type legacyCue struct {
	ID       string  `json:"id"`
	Time     float64 `json:"time"`
	Action   string  `json:"action"`
	EffectID *string `json:"effectId"`
}

type legacyAsset struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Data     string      `json:"data"`
	Duration float64     `json:"duration"`
	Demo     bool        `json:"demo"`
	Builtin  bool        `json:"builtin"`
	Cues     []legacyCue `json:"cues"`
}

type legacyProject struct {
	Name     string        `json:"name"`
	Tracks   []legacyAsset `json:"tracks"`
	Effects  []legacyAsset `json:"effects"`
	Auto     bool          `json:"auto"`
	MusicVol *float64      `json:"musicVol"`
	FxVol    *float64      `json:"fxVol"`
}

type legacyBackup struct {
	Format  string         `json:"format"`
	Project *legacyProject `json:"project"`
	legacyProject
}

var mimeExt = map[string]string{
	"audio/mpeg": "mp3", "audio/mp3": "mp3", "audio/wav": "wav", "audio/x-wav": "wav",
	"audio/wave": "wav", "audio/vnd.wave": "wav", "audio/ogg": "ogg", "audio/opus": "opus",
	"audio/mp4": "m4a", "audio/x-m4a": "m4a", "audio/aac": "aac", "audio/flac": "flac",
	"audio/x-flac": "flac", "audio/webm": "webm",
}

func (s *Store) restoreLegacyJSON(file string) (Project, error) {
	f, err := os.Open(file)
	if err != nil {
		return Project{}, err
	}
	defer f.Close()
	var b legacyBackup
	if err := json.NewDecoder(f).Decode(&b); err != nil {
		return Project{}, fmt.Errorf("arquivo de backup inválido")
	}
	lp := &b.legacyProject
	if b.Project != nil {
		lp = b.Project
	}
	decode := func(a legacyAsset) (string, error) {
		if !strings.HasPrefix(a.Data, "data:") {
			return "", fmt.Errorf("áudio inválido em %q", a.Name)
		}
		comma := strings.IndexByte(a.Data, ',')
		if comma < 0 {
			return "", fmt.Errorf("áudio inválido em %q", a.Name)
		}
		meta := strings.Split(a.Data[5:comma], ";")
		ext := mimeExt[strings.ToLower(meta[0])]
		var r io.Reader = strings.NewReader(a.Data[comma+1:])
		if len(meta) > 1 && meta[len(meta)-1] == "base64" {
			r = base64.NewDecoder(base64.StdEncoding, r)
		}
		return s.ImportAudio(r, ext)
	}
	p := Project{Format: projectFormat, Name: lp.Name, Auto: lp.Auto, MusicVol: 80, FxVol: 80, Tracks: []Track{}, Effects: []Effect{}}
	if lp.MusicVol != nil {
		p.MusicVol = *lp.MusicVol
	}
	if lp.FxVol != nil {
		p.FxVol = *lp.FxVol
	}
	ids := map[string]string{}
	needBuiltins := false
	for _, e := range lp.Effects {
		if e.Demo || e.Builtin || e.Data == "" {
			needBuiltins = true
			continue
		}
		name, err := decode(e)
		if err != nil {
			return Project{}, err
		}
		id := newID()
		ids[e.ID] = id
		p.Effects = append(p.Effects, Effect{ID: id, Name: e.Name, File: name, Duration: e.Duration})
	}
	if needBuiltins || len(lp.Effects) == 0 {
		fx, err := s.builtinEffects()
		if err != nil {
			return Project{}, err
		}
		// efeitos de demonstração da versão de navegador -> efeitos prontos
		for _, e := range lp.Effects {
			if !(e.Demo || e.Builtin || e.Data == "") {
				continue
			}
			for _, b := range fx {
				if strings.EqualFold(b.Name, e.Name) {
					ids[e.ID] = b.ID
				}
			}
		}
		p.Effects = append(fx, p.Effects...)
	}
	for _, t := range lp.Tracks {
		name, err := decode(t)
		if err != nil {
			return Project{}, err
		}
		tr := Track{ID: newID(), Name: t.Name, File: name, Duration: t.Duration, Cues: []Cue{}}
		for _, c := range t.Cues {
			cue := Cue{ID: newID(), Time: c.Time, Action: c.Action}
			if c.Action != "pause" {
				cue.Action = "effect"
				id, ok := "", false
				if c.EffectID != nil {
					id, ok = ids[*c.EffectID]
				}
				if !ok {
					continue
				}
				cue.EffectID = &id
			}
			tr.Cues = append(tr.Cues, cue)
		}
		p.Tracks = append(p.Tracks, tr)
	}
	return p, nil
}

// ------------------------------------------------------------------ util

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

var _ = bytes.MinRead
