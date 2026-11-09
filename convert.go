package main

// Conversão de áudio para um formato que o player (WebView2/Chromium) toca.
//
// Ordem das tentativas para cada arquivo:
//  1. Formatos nativos (MP3, WAV PCM, OGG, Opus, FLAC, AAC/M4A, WebM) -> ficam como estão.
//  2. Decodificadores próprios (Go puro): AIFF/AIFC, WAV especiais (μ-law, A-law,
//     float 64, RF64/W64 não), AU/SND -> WAV PCM.
//  3. Media Foundation (decodificadores do próprio Windows): WMA, ALAC, AC3, AMR,
//     MP2, áudio de vídeos (MP4, MOV, AVI, WMV, MKV com AAC/AC3)... -> WAV PCM.
//  4. ffmpeg.exe, se existir ao lado do programa, na pasta de dados ou no PATH -> WAV.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type kind int

const (
	kindUnknown kind = iota
	kindNative       // o navegador toca direto
	kindMaybe        // contêiner nativo; o codec pode não ser (ex.: M4A com ALAC)
	kindGo           // convertemos em Go
	kindSystem       // precisa de Media Foundation / ffmpeg
	kindNotAudio
)

type probe struct {
	kind  kind
	ext   string // extensão canônica para salvar
	label string
}

var errNotAudio = errors.New("este arquivo não contém áudio gravado")

func sniff(head []byte, ext string) probe {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	h := head
	has := func(off int, s string) bool { return len(h) >= off+len(s) && string(h[off:off+len(s)]) == s }
	switch {
	case has(0, "RIFF") && has(8, "WAVE"):
		return sniffWav(h)
	case has(0, "RIFF") && has(8, "AVI "):
		return probe{kindSystem, "avi", "AVI"}
	case has(0, "RIFF") && has(8, "CDXA"), has(0, "RIFF") && has(8, "RMID"):
		return probe{kindSystem, ext, "RIFF"}
	case has(0, "RF64"), has(0, "BW64"):
		return probe{kindGo, "wav", "WAV RF64"}
	case has(0, "riff\x2e\x91\xcf\x11"):
		return probe{kindSystem, "w64", "Wave64"}
	case has(0, "FORM") && (has(8, "AIFF") || has(8, "AIFC")):
		return probe{kindGo, "aiff", "AIFF"}
	case has(0, ".snd"):
		return probe{kindGo, "au", "AU"}
	case has(0, "fLaC"):
		return probe{kindNative, "flac", "FLAC"}
	case has(0, "OggS"):
		e := "ogg"
		if ext == "opus" || ext == "oga" {
			e = ext
		}
		return probe{kindMaybe, e, "OGG"}
	case has(0, "ID3"):
		return probe{kindMaybe, "mp3", "MP3"}
	case has(4, "ftyp"):
		e := "m4a"
		switch ext {
		case "mp4", "m4a", "m4b", "m4r", "mov", "3gp", "3g2":
			e = ext
		}
		if e == "m4b" || e == "m4r" {
			e = "m4a"
		}
		if e == "mov" || e == "3gp" || e == "3g2" {
			return probe{kindSystem, e, strings.ToUpper(e)}
		}
		return probe{kindMaybe, e, "MP4/M4A"}
	case has(4, "moov"), has(4, "mdat"), has(4, "wide"), has(4, "free"):
		return probe{kindSystem, "mov", "QuickTime"}
	case len(h) >= 4 && binary.BigEndian.Uint32(h) == 0x1A45DFA3:
		e := "webm"
		if ext == "mkv" || ext == "mka" {
			return probe{kindSystem, ext, "Matroska"}
		}
		if ext == "weba" {
			e = ext
		}
		return probe{kindMaybe, e, "WebM"}
	case len(h) >= 16 && bytes.Equal(h[:16], []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11, 0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C}):
		e := "wma"
		if ext == "wmv" || ext == "asf" {
			e = ext
		}
		return probe{kindSystem, e, "WMA"}
	case has(0, "#!AMR"):
		return probe{kindSystem, "amr", "AMR"}
	case has(0, "caff"):
		return probe{kindSystem, "caf", "CAF"}
	case has(0, "MAC "):
		return probe{kindSystem, "ape", "Monkey's Audio"}
	case has(0, "wvpk"):
		return probe{kindSystem, "wv", "WavPack"}
	case has(0, "TTA1"):
		return probe{kindSystem, "tta", "TTA"}
	case has(0, "Creative Voice File"):
		return probe{kindSystem, "voc", "VOC"}
	case has(0, "FLV"):
		return probe{kindSystem, "flv", "FLV"}
	case has(0, ".RMF"), has(0, ".ra\xfd"):
		return probe{kindSystem, "rm", "RealAudio"}
	case has(0, "MThd"):
		return probe{kindNotAudio, ext, "MIDI"}
	case len(h) >= 4 && h[0] == 0 && h[1] == 0 && h[2] == 1 && (h[3] == 0xBA || h[3] == 0xB3):
		return probe{kindSystem, "mpg", "MPEG"}
	case len(h) >= 2 && h[0] == 0x0B && h[1] == 0x77:
		return probe{kindSystem, "ac3", "AC3"}
	case len(h) >= 4 && binary.BigEndian.Uint32(h) == 0x7FFE8001:
		return probe{kindSystem, "dts", "DTS"}
	case len(h) >= 2 && h[0] == 0xFF && h[1]&0xF6 == 0xF0:
		return probe{kindNative, "aac", "AAC"}
	case len(h) >= 2 && h[0] == 0xFF && h[1]&0xE0 == 0xE0:
		// quadro MPEG de áudio: layer III é MP3; layers I/II (MP2) precisam de conversão
		if (h[1]>>1)&3 == 1 {
			return probe{kindNative, "mp3", "MP3"}
		}
		return probe{kindSystem, "mp2", "MP2"}
	case len(h) >= 189 && h[0] == 0x47 && h[188] == 0x47:
		return probe{kindSystem, "ts", "MPEG-TS"}
	}
	// sem assinatura reconhecida: confia na extensão
	switch ext {
	case "mp3":
		return probe{kindMaybe, "mp3", "MP3"}
	case "aac":
		return probe{kindMaybe, "aac", "AAC"}
	case "":
		return probe{kindUnknown, "bin", "?"}
	}
	return probe{kindSystem, ext, strings.ToUpper(ext)}
}

func sniffWav(h []byte) probe {
	tag, bits := wavFormat(h)
	switch {
	case tag == 1 && (bits == 8 || bits == 16 || bits == 24 || bits == 32):
		return probe{kindNative, "wav", "WAV"}
	case tag == 3 && bits == 32:
		return probe{kindNative, "wav", "WAV"}
	case tag == 3 && bits == 64, tag == 6, tag == 7:
		return probe{kindGo, "wav", "WAV"}
	case tag == 0x55:
		return probe{kindNative, "mp3", "WAV/MP3"} // MP3 dentro de WAV: tentamos direto
	}
	return probe{kindSystem, "wav", "WAV compactado"}
}

// wavFormat lê a tag de formato (resolvendo WAVE_FORMAT_EXTENSIBLE) e os bits.
func wavFormat(h []byte) (tag, bits int) {
	for i := 12; i+8 <= len(h); {
		size := int(binary.LittleEndian.Uint32(h[i+4 : i+8]))
		if string(h[i:i+4]) == "fmt " && i+8+16 <= len(h) {
			b := h[i+8:]
			tag = int(binary.LittleEndian.Uint16(b[0:2]))
			bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if tag == 0xFFFE && len(b) >= 26 {
				tag = int(binary.LittleEndian.Uint16(b[24:26]))
			}
			return
		}
		if size < 0 || size > len(h) {
			break
		}
		i += 8 + size + size&1
	}
	return 0, 0
}

// ------------------------------------------------------------------ orquestração

type Converter struct {
	ffmpeg string // caminho do ffmpeg.exe, se encontrado
	logf   func(string, ...any)
}

func newConverter(dataDir string, logf func(string, ...any)) *Converter {
	c := &Converter{logf: logf}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(d, ffmpegName), filepath.Join(d, "ffmpeg", "bin", ffmpegName), filepath.Join(d, "ffmpeg", ffmpegName))
	}
	candidates = append(candidates, filepath.Join(dataDir, ffmpegName))
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			c.ffmpeg = p
			break
		}
	}
	if c.ffmpeg == "" {
		if p, err := exec.LookPath(ffmpegName); err == nil {
			c.ffmpeg = p
		}
	}
	return c
}

// ToWav converte src (de qualquer formato) para dst (WAV PCM).
// hint é a extensão original, usada pelo Media Foundation e pelo ffmpeg.
func (c *Converter) ToWav(src, dst string, p probe, display string) error {
	var errs []string
	if p.kind == kindNotAudio {
		return errNotAudio
	}
	if p.kind == kindGo {
		err := convertWithGo(src, dst)
		if err == nil {
			return nil
		}
		errs = append(errs, "interno: "+err.Error())
	}
	if err := mediaFoundationToWav(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, errUnsupportedOS) {
		errs = append(errs, "Windows: "+err.Error())
	}
	if c.ffmpeg != "" {
		err := c.ffmpegToWav(src, dst)
		if err == nil {
			return nil
		}
		errs = append(errs, "ffmpeg: "+err.Error())
	}
	os.Remove(dst)
	c.logf("conversão falhou para %s (%s): %s", display, p.label, strings.Join(errs, " | "))
	if c.ffmpeg == "" {
		return fmt.Errorf("formato %s não suportado pelo Windows deste computador; coloque o ffmpeg.exe na pasta do programa para abrir qualquer formato", p.label)
	}
	return fmt.Errorf("formato %s não suportado neste computador", p.label)
}

func (c *Converter) ffmpegToWav(src, dst string) error {
	cmd := exec.Command(c.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", src, "-map", "0:a:0", "-vn", "-c:a", "pcm_s16le", "-f", "wav", dst)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

// ------------------------------------------------------------------ WAV

type wavWriter struct {
	f          *os.File
	ch, rate   int
	bits       int
	float      bool
	dataBytes  int64
	headerSize int64
}

func createWav(path string, ch, rate, bits int, float bool) (*wavWriter, error) {
	if ch < 1 || rate < 1 || (bits != 8 && bits != 16 && bits != 24 && bits != 32) {
		return nil, fmt.Errorf("formato de saída inválido (%d canais, %d Hz, %d bits)", ch, rate, bits)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &wavWriter{f: f, ch: ch, rate: rate, bits: bits, float: float, headerSize: 44}
	if _, err := f.Write(make([]byte, 44)); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *wavWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.dataBytes += int64(n)
	return n, err
}

func (w *wavWriter) Close() error {
	if w.dataBytes&1 == 1 {
		w.f.Write([]byte{0})
	}
	tag := uint16(1)
	if w.float {
		tag = 3
	}
	block := w.ch * w.bits / 8
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+w.dataBytes+w.dataBytes&1))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], tag)
	binary.LittleEndian.PutUint16(h[22:], uint16(w.ch))
	binary.LittleEndian.PutUint32(h[24:], uint32(w.rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(w.rate*block))
	binary.LittleEndian.PutUint16(h[32:], uint16(block))
	binary.LittleEndian.PutUint16(h[34:], uint16(w.bits))
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(w.dataBytes))
	_, err := w.f.WriteAt(h, 0)
	if err == nil {
		err = w.f.Sync()
	}
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	if err == nil && w.dataBytes == 0 {
		err = errors.New("nenhum áudio decodificado")
	}
	return err
}

// ------------------------------------------------------------------ decodificadores em Go

type pcmSpec struct {
	ch, rate, bits int
	bigEndian      bool
	float          bool
	signed         bool
	law            byte // 'u' μ-law, 'a' A-law
}

func convertWithGo(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return err
	}
	f.Seek(0, io.SeekStart)
	var spec pcmSpec
	var data io.Reader
	switch {
	case string(head[0:4]) == "FORM":
		spec, data, err = parseAIFF(f)
	case string(head[0:4]) == ".snd":
		spec, data, err = parseAU(f)
	case string(head[0:4]) == "RIFF" || string(head[0:4]) == "RF64" || string(head[0:4]) == "BW64":
		spec, data, err = parseWAV(f)
	default:
		err = errors.New("formato desconhecido")
	}
	if err != nil {
		return err
	}
	return writePCM(dst, spec, data)
}

// writePCM normaliza para WAV little-endian: 8 bits -> 8, 16 -> 16, 24 -> 24,
// 32 int -> 32, float -> float 32, μ/A-law -> 16.
func writePCM(dst string, s pcmSpec, r io.Reader) error {
	outBits, outFloat := s.bits, false
	inBytes := (s.bits + 7) / 8
	switch {
	case s.law != 0:
		outBits, inBytes = 16, 1
	case s.float:
		outBits, outFloat = 32, true
	case s.bits <= 8:
		outBits = 8
	case s.bits <= 16:
		outBits = 16
	case s.bits <= 24:
		outBits = 24
	default:
		outBits = 32
	}
	w, err := createWav(dst, s.ch, s.rate, outBits, outFloat)
	if err != nil {
		return err
	}
	frame := inBytes * s.ch
	in := make([]byte, frame*4096)
	out := make([]byte, 0, len(in)*2)
	carry := 0
	for {
		n, rerr := io.ReadFull(r, in[carry:])
		n += carry
		whole := n - n%inBytes
		out = out[:0]
		for i := 0; i < whole; i += inBytes {
			out = appendSample(out, in[i:i+inBytes], s, outBits)
		}
		if _, err := w.Write(out); err != nil {
			w.Close()
			return err
		}
		carry = copy(in, in[whole:n])
		if rerr != nil {
			break
		}
	}
	return w.Close()
}

func appendSample(out, b []byte, s pcmSpec, outBits int) []byte {
	if s.law != 0 {
		var v int16
		if s.law == 'u' {
			v = ulaw(b[0])
		} else {
			v = alaw(b[0])
		}
		return binary.LittleEndian.AppendUint16(out, uint16(v))
	}
	if s.float {
		var f float32
		if len(b) == 8 {
			var u uint64
			if s.bigEndian {
				u = binary.BigEndian.Uint64(b)
			} else {
				u = binary.LittleEndian.Uint64(b)
			}
			f = float32(math.Float64frombits(u))
		} else {
			var u uint32
			if s.bigEndian {
				u = binary.BigEndian.Uint32(b)
			} else {
				u = binary.LittleEndian.Uint32(b)
			}
			f = math.Float32frombits(u)
		}
		return binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
	}
	// inteiro: monta valor com sinal alinhado à esquerda em 32 bits
	var v int32
	n := len(b)
	for i := 0; i < n; i++ {
		var byt byte
		if s.bigEndian {
			byt = b[i]
		} else {
			byt = b[n-1-i]
		}
		v |= int32(uint32(byt) << (24 - 8*i))
	}
	if n == 1 && !s.signed {
		v ^= -0x80000000 // 8 bits sem sinal -> com sinal
	}
	switch outBits {
	case 8:
		return append(out, byte(v>>24)+0x80) // WAV de 8 bits é sem sinal
	case 16:
		return binary.LittleEndian.AppendUint16(out, uint16(v>>16))
	case 24:
		return append(out, byte(v>>8), byte(v>>16), byte(v>>24))
	default:
		return binary.LittleEndian.AppendUint32(out, uint32(v))
	}
}

func ulaw(u byte) int16 {
	u = ^u
	t := (int(u&0x0F) << 3) + 0x84
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return int16(0x84 - t)
	}
	return int16(t - 0x84)
}

func alaw(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

func parseAIFF(f *os.File) (pcmSpec, io.Reader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return pcmSpec{}, nil, err
	}
	aifc := string(hdr[8:12]) == "AIFC"
	s := pcmSpec{bigEndian: true, signed: true}
	gotComm := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return pcmSpec{}, nil, errors.New("AIFF sem dados de áudio")
		}
		id := string(ch[0:4])
		size := int64(binary.BigEndian.Uint32(ch[4:8]))
		switch id {
		case "COMM":
			b := make([]byte, size)
			if _, err := io.ReadFull(f, b); err != nil || size < 18 {
				return pcmSpec{}, nil, errors.New("AIFF inválido")
			}
			s.ch = int(binary.BigEndian.Uint16(b[0:2]))
			s.bits = int(binary.BigEndian.Uint16(b[6:8]))
			s.rate = int(math.Round(ieee80(b[8:18])))
			if aifc && size >= 22 {
				switch strings.ToLower(string(b[18:22])) {
				case "none", "twos":
				case "sowt":
					s.bigEndian = false
				case "fl32":
					s.float, s.bits = true, 32
				case "fl64":
					s.float, s.bits = true, 64
				case "ulaw":
					s.law, s.bits = 'u', 8
				case "alaw":
					s.law, s.bits = 'a', 8
				case "raw ":
					s.signed = false
				case "in24":
					s.bits = 24
				case "in32":
					s.bits = 32
				case "42ni":
					s.bits, s.bigEndian = 24, false
				case "23ni":
					s.bits, s.bigEndian = 32, false
				default:
					return pcmSpec{}, nil, fmt.Errorf("AIFF compactado (%s)", string(b[18:22]))
				}
			}
			if size&1 == 1 {
				f.Seek(1, io.SeekCurrent)
			}
			gotComm = true
		case "SSND":
			if !gotComm {
				return pcmSpec{}, nil, errors.New("AIFF fora de ordem")
			}
			var off [8]byte
			if _, err := io.ReadFull(f, off[:]); err != nil {
				return pcmSpec{}, nil, err
			}
			skip := int64(binary.BigEndian.Uint32(off[0:4]))
			f.Seek(skip, io.SeekCurrent)
			if s.ch < 1 || s.rate < 1 || s.bits < 1 || s.bits > 64 {
				return pcmSpec{}, nil, errors.New("AIFF inválido")
			}
			return s, io.LimitReader(f, size-8-skip), nil
		default:
			f.Seek(size+size&1, io.SeekCurrent)
		}
	}
}

func ieee80(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:10])
	if exp == 0 && mant == 0 {
		return 0
	}
	v := float64(mant) * math.Pow(2, float64(exp-16383-63))
	if b[0]&0x80 != 0 {
		v = -v
	}
	return v
}

func parseAU(f *os.File) (pcmSpec, io.Reader, error) {
	var h [24]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return pcmSpec{}, nil, err
	}
	off := int64(binary.BigEndian.Uint32(h[4:8]))
	size := int64(binary.BigEndian.Uint32(h[8:12]))
	enc := binary.BigEndian.Uint32(h[12:16])
	s := pcmSpec{rate: int(binary.BigEndian.Uint32(h[16:20])), ch: int(binary.BigEndian.Uint32(h[20:24])), bigEndian: true, signed: true}
	switch enc {
	case 1:
		s.law, s.bits = 'u', 8
	case 2:
		s.bits = 8
	case 3:
		s.bits = 16
	case 4:
		s.bits = 24
	case 5:
		s.bits = 32
	case 6:
		s.float, s.bits = true, 32
	case 7:
		s.float, s.bits = true, 64
	case 27:
		s.law, s.bits = 'a', 8
	default:
		return pcmSpec{}, nil, fmt.Errorf("AU com codificação %d", enc)
	}
	if s.ch < 1 || s.ch > 32 || s.rate < 1 {
		return pcmSpec{}, nil, errors.New("AU inválido")
	}
	f.Seek(off, io.SeekStart)
	var r io.Reader = f
	if size != 0xFFFFFFFF {
		r = io.LimitReader(f, size)
	}
	return s, r, nil
}

func parseWAV(f *os.File) (pcmSpec, io.Reader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return pcmSpec{}, nil, err
	}
	rf64 := string(hdr[0:4]) != "RIFF"
	var ds64Data int64 = -1
	s := pcmSpec{signed: true}
	gotFmt := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return pcmSpec{}, nil, errors.New("WAV sem dados de áudio")
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "ds64":
			b := make([]byte, size)
			io.ReadFull(f, b)
			if len(b) >= 16 {
				ds64Data = int64(binary.LittleEndian.Uint64(b[8:16]))
			}
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(f, b); err != nil || size < 16 {
				return pcmSpec{}, nil, errors.New("WAV inválido")
			}
			if size&1 == 1 {
				f.Seek(1, io.SeekCurrent)
			}
			tag := int(binary.LittleEndian.Uint16(b[0:2]))
			if tag == 0xFFFE && len(b) >= 26 {
				tag = int(binary.LittleEndian.Uint16(b[24:26]))
			}
			s.ch = int(binary.LittleEndian.Uint16(b[2:4]))
			s.rate = int(binary.LittleEndian.Uint32(b[4:8]))
			s.bits = int(binary.LittleEndian.Uint16(b[14:16]))
			switch tag {
			case 1:
				if s.bits <= 8 {
					s.signed = false
				}
			case 3:
				s.float = true
			case 6:
				s.law, s.bits = 'a', 8
			case 7:
				s.law, s.bits = 'u', 8
			default:
				return pcmSpec{}, nil, fmt.Errorf("WAV compactado (formato %#x)", tag)
			}
			gotFmt = true
		case "data":
			if !gotFmt {
				return pcmSpec{}, nil, errors.New("WAV fora de ordem")
			}
			if rf64 && size == 0xFFFFFFFF && ds64Data >= 0 {
				size = ds64Data
			}
			if s.ch < 1 || s.rate < 1 || s.bits < 1 || s.bits > 64 {
				return pcmSpec{}, nil, errors.New("WAV inválido")
			}
			return s, io.LimitReader(f, size), nil
		default:
			f.Seek(size+size&1, io.SeekCurrent)
		}
	}
}
