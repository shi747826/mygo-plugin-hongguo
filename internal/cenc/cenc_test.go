package cenc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"testing"
)

// box builds an ISO BMFF box from its type and body.
func build(typ string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

// encryptCTR produces cenc ciphertext for a whole sample.
func encryptCTR(key, iv, plain []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	counter := make([]byte, 16)
	copy(counter, iv)
	out := make([]byte, len(plain))
	cipher.NewCTR(block, counter).XORKeyStream(out, plain)
	return out
}

func u32(v int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}

// fixture is a one-track MP4 whose two samples are cenc encrypted.
func fixture(key []byte, plain [][]byte) ([]byte, [][]byte) {
	var ivs [][]byte
	var samples [][]byte
	for i, p := range plain {
		iv := []byte{0, 0, 0, 0, 0, 0, 0, byte(i + 1)}
		ivs = append(ivs, iv)
		samples = append(samples, encryptCTR(key, iv, p))
	}
	// stsd holds one visual sample entry with sinf/schi/tenc.
	tenc := build("tenc", append([]byte{0, 0, 0, 0, 0, 1, 8}, make([]byte, 16)...))
	sinf := build("sinf", bytes.Join([][]byte{build("frma", []byte("hvc1")), build("schm", append([]byte{0, 0, 0, 0}, []byte("cenc")...)), build("schi", tenc)}, nil))
	entryBody := make([]byte, 78)
	copy(entryBody[24:26], []byte{0x05, 0x00}) // width
	entry := build("encv", append(entryBody, sinf...))
	stsd := build("stsd", append(append([]byte{0, 0, 0, 0}, u32(1)...), entry...))
	stsz := build("stsz", append(append([]byte{0, 0, 0, 0}, u32(0)...), append(u32(len(plain)), sampleSizesBytes(samples)...)...))
	stsc := build("stsc", append(append([]byte{0, 0, 0, 0}, u32(1)...), append(u32(1), append(u32(len(plain)), u32(1)...)...)...))
	// The mdat sits after moov: compute its payload offset first.
	mdatPayload := int64(0)
	for _, s := range samples {
		mdatPayload += int64(len(s))
	}
	stcoBody := append([]byte{0, 0, 0, 0}, u32(1)...)
	stco := build("stco", append(stcoBody, u32(0xdeadbeef)...)) // patched below
	sencBody := append([]byte{0, 0, 0, 0}, u32(len(ivs))...)
	for _, iv := range ivs {
		sencBody = append(sencBody, iv...)
	}
	senc := build("senc", sencBody)
	saiz := build("saiz", append([]byte{0, 0, 0, 0, 8}, u32(len(ivs))...))
	stbl := build("stbl", bytes.Join([][]byte{stsd, stsz, stsc, stco, senc, saiz}, nil))
	minf := build("minf", stbl)
	mdia := build("mdia", minf)
	trak := build("trak", mdia)
	moov := build("moov", append(build("mvhd", make([]byte, 100)), trak...))
	ftyp := build("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	mdat := build("mdat", bytes.Join(samples, nil))
	// The first sample starts after ftyp+moov and the mdat header.
	first := len(ftyp) + len(moov) + 8
	patched := bytes.Replace(moov, u32(0xdeadbeef), u32(first), 1)
	file := append(append(ftyp, patched...), mdat...)
	_ = mdatPayload
	return file, plain
}

func sampleSizesBytes(samples [][]byte) []byte {
	var out []byte
	for _, s := range samples {
		out = append(out, u32(len(s))...)
	}
	return out
}

func TestDecryptWholeSamples(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := [][]byte{[]byte("\x00\x00\x00\x04hevc-sample-one-payload"), []byte("\x00\x00\x00\x04second-sample-payload!!")}
	file, want := fixture(key, plain)
	got, err := Decrypt(file, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(got) != len(file) {
		t.Fatalf("length changed: %d -> %d", len(file), len(got))
	}
	for i, w := range want {
		if !bytes.Contains(got, w) {
			t.Fatalf("sample %d was not decrypted", i)
		}
	}
	for _, typ := range []string{"encv", "sinf", "senc", "saiz", "tenc"} {
		if bytes.Contains(got, []byte(typ)) {
			t.Fatalf("box %q survived decryption", typ)
		}
	}
	if !bytes.Contains(got, []byte("hvc1")) || !bytes.Contains(got, []byte("mdat")) {
		t.Fatal("codec or media box lost")
	}
}

func TestDecryptRejectsBadInput(t *testing.T) {
	if _, err := Decrypt([]byte("not an mp4"), []byte("0123456789abcdef")); err != ErrMalformed {
		t.Fatalf("garbage error = %v", err)
	}
	file, _ := fixture([]byte("0123456789abcdef"), [][]byte{[]byte("payload")})
	if _, err := Decrypt(file, []byte("short")); err != ErrUnsupported {
		t.Fatalf("short key error = %v", err)
	}
	plainMP4 := build("ftyp", []byte("isom"))
	plainMP4 = append(plainMP4, build("moov", build("trak", nil))...)
	if _, err := Decrypt(plainMP4, []byte("0123456789abcdef")); err != ErrUnsupported {
		t.Fatalf("plain MP4 error = %v", err)
	}
}
