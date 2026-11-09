//go:build windows

package main

// Decodificação com o Media Foundation (componente de mídia do próprio Windows).
// Usa o Source Reader para entregar PCM, que gravamos em WAV. Cobre os formatos
// que o Windows sabe abrir: WMA (todas as versões), ALAC, AAC, AC3/E-AC3, AMR,
// MP2, áudio de vídeos MP4/MOV/AVI/WMV/MKV, e qualquer codec extra instalado.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ffmpegName = "ffmpeg.exe"

var errUnsupportedOS = errors.New("Media Foundation indisponível")

var (
	modOle32       = windows.NewLazySystemDLL("ole32.dll")
	procCoInit     = modOle32.NewProc("CoInitializeEx")
	procCoUninit   = modOle32.NewProc("CoUninitialize")
	modMfplat      = windows.NewLazySystemDLL("mfplat.dll")
	procMFStartup  = modMfplat.NewProc("MFStartup")
	procMFShutdown = modMfplat.NewProc("MFShutdown")
	procMFCreateMT = modMfplat.NewProc("MFCreateMediaType")
	modMfread      = windows.NewLazySystemDLL("mfreadwrite.dll")
	procMFReader   = modMfread.NewProc("MFCreateSourceReaderFromURL")
)

var (
	guidMajorType  = windows.GUID{Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687, Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	guidSubtype    = windows.GUID{Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714, Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	guidAudio      = windows.GUID{Data1: 0x73647561, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	guidPCM        = windows.GUID{Data1: 0x00000001, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	guidChannels   = windows.GUID{Data1: 0x37e48bf5, Data2: 0x645e, Data3: 0x4c5b, Data4: [8]byte{0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x69, 0x6a}}
	guidSampleRate = windows.GUID{Data1: 0x5faeeae7, Data2: 0x0290, Data3: 0x4c31, Data4: [8]byte{0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x9d, 0xba}}
	guidBits       = windows.GUID{Data1: 0xf2deb57f, Data2: 0x40fa, Data3: 0x4764, Data4: [8]byte{0xaa, 0x33, 0xed, 0x4f, 0x2d, 0x1f, 0xf6, 0x69}}
)

const (
	mfVersion          = 0x00020070
	streamAll          = 0xFFFFFFFE
	streamFirstAudio   = 0xFFFFFFFD
	flagError          = 0x1
	flagEndOfStream    = 0x2
	flagCurrentTypeChg = 0x20
)

// índices das funções nas vtables COM
const (
	vRelease = 2
	// IMFSourceReader
	vSetStreamSelection  = 4
	vGetCurrentMediaType = 6
	vSetCurrentMediaType = 7
	vReadSample          = 9
	// IMFAttributes
	vGetUINT32 = 7
	vSetUINT32 = 21
	vSetGUID   = 24
	// IMFSample
	vConvertToContiguousBuffer = 41
	// IMFMediaBuffer
	vLock   = 3
	vUnlock = 4
)

type comObj uintptr

func (o comObj) call(idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(o))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o)}, args...)...)
	return r
}

func (o comObj) release() {
	if o != 0 {
		o.call(vRelease)
	}
}

func hr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s (0x%08X)", what, uint32(r))
	}
	return nil
}

var mfMu sync.Mutex // uma conversão por vez: simples e previsível

func mediaFoundationToWav(src, dst string) (err error) {
	if procMFReader.Find() != nil || procMFStartup.Find() != nil {
		return errors.New("Media Foundation não instalado (Windows N: instale o Media Feature Pack)")
	}
	mfMu.Lock()
	defer mfMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, _, _ := procCoInit.Call(0, 0 /* COINIT_MULTITHREADED */); int32(r) >= 0 {
		defer procCoUninit.Call()
	}
	if r, _, _ := procMFStartup.Call(mfVersion, 0); int32(r) < 0 {
		return hr(r, "MFStartup")
	}
	defer procMFShutdown.Call()

	url, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	pReader := new(comObj)
	r0, _, _ := procMFReader.Call(uintptr(unsafe.Pointer(url)), 0, uintptr(unsafe.Pointer(pReader)))
	runtime.KeepAlive(url)
	if int32(r0) < 0 {
		return hr(r0, "o Windows não reconhece este formato")
	}
	reader := *pReader
	defer reader.release()

	reader.call(vSetStreamSelection, streamAll, 0)
	if e := hr(reader.call(vSetStreamSelection, streamFirstAudio, 1), "arquivo sem trilha de áudio"); e != nil {
		return e
	}

	pPartial := new(comObj)
	if r, _, _ := procMFCreateMT.Call(uintptr(unsafe.Pointer(pPartial))); int32(r) < 0 {
		return hr(r, "MFCreateMediaType")
	}
	partial := *pPartial
	partial.call(vSetGUID, uintptr(unsafe.Pointer(&guidMajorType)), uintptr(unsafe.Pointer(&guidAudio)))
	partial.call(vSetGUID, uintptr(unsafe.Pointer(&guidSubtype)), uintptr(unsafe.Pointer(&guidPCM)))
	r := reader.call(vSetCurrentMediaType, streamFirstAudio, 0, uintptr(partial))
	partial.release()
	if e := hr(r, "sem decodificador para este áudio"); e != nil {
		return e
	}

	pCur := new(comObj)
	if e := hr(reader.call(vGetCurrentMediaType, streamFirstAudio, uintptr(unsafe.Pointer(pCur))), "GetCurrentMediaType"); e != nil {
		return e
	}
	cur := *pCur
	fmtv := new([3]uint32) // canais, taxa, bits (na heap: não se move durante as chamadas)
	cur.call(vGetUINT32, uintptr(unsafe.Pointer(&guidChannels)), uintptr(unsafe.Pointer(&fmtv[0])))
	cur.call(vGetUINT32, uintptr(unsafe.Pointer(&guidSampleRate)), uintptr(unsafe.Pointer(&fmtv[1])))
	cur.call(vGetUINT32, uintptr(unsafe.Pointer(&guidBits)), uintptr(unsafe.Pointer(&fmtv[2])))
	cur.release()
	ch, rate, bits := fmtv[0], fmtv[1], fmtv[2]
	if bits == 0 {
		bits = 16
	}

	w, err := createWav(dst, int(ch), int(rate), int(bits), false)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			w.Close()
			os.Remove(dst)
		}
	}()

	type readOut struct {
		idx, flags uint32
		ts         int64
		sample     comObj
		buf        comObj
		p          uintptr
		max, n     uint32
	}
	out := new(readOut) // na heap: os ponteiros passados ao COM continuam válidos
	for {
		*out = readOut{}
		r := reader.call(vReadSample, streamFirstAudio, 0,
			uintptr(unsafe.Pointer(&out.idx)), uintptr(unsafe.Pointer(&out.flags)),
			uintptr(unsafe.Pointer(&out.ts)), uintptr(unsafe.Pointer(&out.sample)))
		flags, sample := out.flags, out.sample
		if e := hr(r, "erro ao decodificar"); e != nil {
			sample.release()
			return e
		}
		if flags&flagError != 0 {
			sample.release()
			return errors.New("erro ao ler o arquivo")
		}
		if flags&flagCurrentTypeChg != 0 {
			sample.release()
			return errors.New("o formato do áudio muda no meio do arquivo")
		}
		if sample != 0 {
			if e := hr(sample.call(vConvertToContiguousBuffer, uintptr(unsafe.Pointer(&out.buf))), "ConvertToContiguousBuffer"); e != nil {
				sample.release()
				return e
			}
			buf := out.buf
			if e := hr(buf.call(vLock, uintptr(unsafe.Pointer(&out.p)), uintptr(unsafe.Pointer(&out.max)), uintptr(unsafe.Pointer(&out.n))), "Lock"); e != nil {
				buf.release()
				sample.release()
				return e
			}
			if out.n > 0 && out.p != 0 {
				_, err = w.Write(unsafe.Slice((*byte)(unsafe.Pointer(out.p)), out.n))
			}
			buf.call(vUnlock)
			buf.release()
			sample.release()
			if err != nil {
				return err
			}
		}
		if flags&flagEndOfStream != 0 {
			break
		}
	}
	runtime.KeepAlive(out)
	runtime.KeepAlive(fmtv)
	return w.Close()
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 /* CREATE_NO_WINDOW */}
}
