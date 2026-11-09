package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoDecoders(t *testing.T) {
	dir := os.Getenv("TESTFILES")
	if dir == "" {
		t.Skip()
	}
	out := os.Getenv("TESTOUT")
	for _, n := range []string{"aiff16.aiff", "aiff24.aif", "aifc_sowt.aifc", "wav_ulaw.wav", "wav_alaw.wav", "wav_f64.wav", "som.au", "wav_u8.wav"} {
		src := filepath.Join(dir, n)
		head := make([]byte, 4096)
		f, _ := os.Open(src)
		k, _ := f.Read(head)
		f.Close()
		p := sniff(head[:k], filepath.Ext(n))
		dst := filepath.Join(out, n+".out.wav")
		err := convertWithGo(src, dst)
		t.Logf("%-16s kind=%d label=%-8s err=%v", n, p.kind, p.label, err)
	}
}
