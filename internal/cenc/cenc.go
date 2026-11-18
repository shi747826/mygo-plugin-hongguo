// Package cenc decrypts ISO Common Encryption (cenc) MP4 files with a
// content key, so a platform without the desktop's FFmpeg can play them.
//
// The media this package handles is an MP4 whose tracks use the cenc scheme
// with whole-sample AES-CTR encryption: each sample names its 8 or 16 byte
// IV in its track's senc box. After decrypting, the encryption boxes are
// turned into free boxes, which keeps every box size unchanged, and the
// encrypted sample entries name their real codec.
package cenc

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

// ErrUnsupported classifies media this package cannot decrypt.
var ErrUnsupported = errors.New("cenc: unsupported encryption layout")

// ErrMalformed classifies data that is not a readable MP4.
var ErrMalformed = errors.New("cenc: malformed MP4")

type box struct {
	typ        string
	start, end int
	body       int
}

// Decrypt returns data with every encrypted sample decrypted in place and the
// encryption metadata neutralised. The input is not modified.
func Decrypt(data, key []byte) ([]byte, error) {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, ErrUnsupported
	}
	out := append([]byte(nil), data...)
	moov, err := findBox(out, 0, len(out), "moov")
	if err != nil {
		return nil, err
	}
	boxSize := moov.end - moov.start
	// Sample offsets are absolute, so decrypting must not move anything:
	// the boxes below are rewritten in place.
	_ = boxSize
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	traks := children(out, moov)
	found := false
	for _, trak := range traks {
		if trak.typ != "trak" {
			continue
		}
		ok, err := decryptTrack(out, trak, block)
		if err != nil {
			return nil, err
		}
		found = found || ok
	}
	if !found {
		return nil, ErrUnsupported
	}
	neutralize(out)
	return out, nil
}

func decryptTrack(data []byte, trak box, block cipher.Block) (bool, error) {
	mdia, err := findChild(data, trak, "mdia")
	if err != nil {
		return false, nil
	}
	minf, err := findChild(data, mdia, "minf")
	if err != nil {
		return false, nil
	}
	stbl, err := findChild(data, minf, "stbl")
	if err != nil {
		return false, nil
	}
	stsz, err := findChild(data, stbl, "stsz")
	if err != nil {
		return false, nil
	}
	stsc, err := findChild(data, stbl, "stsc")
	if err != nil {
		return false, nil
	}
	stco, err := findChild(data, stbl, "stco")
	if err != nil {
		if stco, err = findChild(data, stbl, "co64"); err != nil {
			return false, nil
		}
	}
	// Plain tracks have no senc: nothing to decrypt there.
	if _, err := findChild(data, stbl, "senc"); err != nil {
		return false, nil
	}
	senc, _ := findChild(data, stbl, "senc")
	sizes, err := sampleSizes(data, stsz)
	if err != nil {
		return false, err
	}
	chunks, err := chunkOffsets(data, stco)
	if err != nil {
		return false, err
	}
	starts, err := sampleStarts(data, stsc, chunks, sizes)
	if err != nil {
		return false, err
	}
	ivSize, err := sampleIVSize(data, stbl)
	if err != nil {
		return false, err
	}
	if len(starts) != len(sizes) {
		return false, ErrMalformed
	}
	ivs, err := sampleIVs(data, senc, len(sizes), ivSize)
	if err != nil {
		return false, err
	}
	for i, start := range starts {
		size := sizes[i]
		if start < 0 || size < 0 || start+size > len(data) {
			return false, ErrMalformed
		}
		counter := make([]byte, aes.BlockSize)
		copy(counter, ivs[i])
		if len(ivs[i]) == 8 {
			// cenc pads an 8 byte IV with zeros to the block size.
			copy(counter[8:], make([]byte, 8))
		}
		cipher.NewCTR(block, counter).XORKeyStream(data[start:start+size], data[start:start+size])
	}
	return true, nil
}

// sampleIVSize reads the per-sample IV length from the track's saiz default,
// which is the IV alone when the samples have no subsample encryption.
func sampleIVSize(data []byte, stbl box) (int, error) {
	saiz, err := findChild(data, stbl, "saiz")
	if err != nil {
		return 0, ErrUnsupported
	}
	body := data[saiz.body:saiz.end]
	if len(body) < 5 {
		return 0, ErrMalformed
	}
	if body[0] != 0 {
		return 0, ErrUnsupported
	}
	size := int(body[4])
	// A default of 0 means the sizes are listed after the count; read the
	// first entry, which every sample shares in these files.
	if size == 0 {
		if len(body) < 9 {
			return 0, ErrMalformed
		}
		count := int(binary.BigEndian.Uint32(body[5:9]))
		if count == 0 || len(body) < 10 {
			return 0, ErrMalformed
		}
		size = int(body[9])
	}
	if size != 8 && size != 16 {
		// Subsample encryption (or a constant IV) needs a different layout.
		return 0, ErrUnsupported
	}
	return size, nil
}

func sampleSizes(data []byte, stsz box) ([]int, error) {
	body := data[stsz.body:stsz.end]
	if len(body) < 12 {
		return nil, ErrMalformed
	}
	uniform := int(binary.BigEndian.Uint32(body[4:8]))
	count := int(binary.BigEndian.Uint32(body[8:12]))
	if count < 0 || count > 1<<24 {
		return nil, ErrMalformed
	}
	sizes := make([]int, count)
	if uniform != 0 {
		for i := range sizes {
			sizes[i] = uniform
		}
		return sizes, nil
	}
	if len(body) < 12+4*count {
		return nil, ErrMalformed
	}
	for i := range sizes {
		sizes[i] = int(binary.BigEndian.Uint32(body[12+4*i:]))
	}
	return sizes, nil
}

func chunkOffsets(data []byte, stco box) ([]int, error) {
	body := data[stco.body:stco.end]
	if len(body) < 8 {
		return nil, ErrMalformed
	}
	count := int(binary.BigEndian.Uint32(body[4:8]))
	if count < 0 || count > 1<<24 {
		return nil, ErrMalformed
	}
	width := 4
	if stco.typ == "co64" {
		width = 8
	}
	if len(body) < 8+width*count {
		return nil, ErrMalformed
	}
	out := make([]int, count)
	for i := range out {
		if width == 8 {
			out[i] = int(binary.BigEndian.Uint64(body[8+8*i:]))
		} else {
			out[i] = int(binary.BigEndian.Uint32(body[8+4*i:]))
		}
	}
	return out, nil
}

func sampleStarts(data []byte, stsc box, chunks, sizes []int) ([]int, error) {
	body := data[stsc.body:stsc.end]
	if len(body) < 8 {
		return nil, ErrMalformed
	}
	count := int(binary.BigEndian.Uint32(body[4:8]))
	if count < 0 || count > 1<<20 || len(body) < 8+12*count {
		return nil, ErrMalformed
	}
	type entry struct{ first, per int }
	entries := make([]entry, count)
	for i := range entries {
		entries[i] = entry{
			first: int(binary.BigEndian.Uint32(body[8+12*i:])),
			per:   int(binary.BigEndian.Uint32(body[12+12*i:])),
		}
	}
	if len(entries) == 0 {
		return nil, ErrMalformed
	}
	starts := make([]int, 0, len(sizes))
	sample := 0
	for i, offset := range chunks {
		per := 0
		for _, e := range entries {
			if e.first-1 <= i {
				per = e.per
			}
		}
		if per < 0 || per > len(sizes) {
			return nil, ErrMalformed
		}
		pos := offset
		for n := 0; n < per && sample < len(sizes); n++ {
			starts = append(starts, pos)
			pos += sizes[sample]
			sample++
		}
	}
	if sample < len(sizes) {
		return nil, ErrMalformed
	}
	return starts, nil
}

func sampleIVs(data []byte, senc box, count, ivSize int) ([][]byte, error) {
	body := data[senc.body:senc.end]
	if len(body) < 8 {
		return nil, ErrMalformed
	}
	flags := body[1:4]
	listed := int(binary.BigEndian.Uint32(body[4:8]))
	if listed != count {
		return nil, ErrMalformed
	}
	if len(flags) == 3 && flags[2]&0x02 != 0 {
		// The override flag means sample_count follows this field instead.
		return nil, ErrUnsupported
	}
	if len(body) < 8+ivSize*count {
		return nil, ErrMalformed
	}
	ivs := make([][]byte, count)
	for i := range ivs {
		ivs[i] = body[8+ivSize*i : 8+ivSize*(i+1)]
	}
	return ivs, nil
}

// neutralize keeps every box where it is and only relabels the encryption
// boxes: players skip free boxes and read the sample entries' real codec.
func neutralize(data []byte) {
	relabel := map[string]string{
		"encv": "hvc1", "enca": "mp4a",
		"sinf": "free", "senc": "free", "saiz": "free",
		"saio": "free", "sgpd": "free", "sbgp": "free",
		"frma": "free", "schm": "free", "schi": "free", "tenc": "free",
	}
	var walk func(start, end int)
	walk = func(start, end int) {
		for _, b := range parse(data, start, end) {
			if to, ok := relabel[b.typ]; ok {
				copy(data[b.start+4:b.start+8], to)
			}
			switch b.typ {
			case "stsd":
				// Entries keep their codec header before their child boxes.
				for _, e := range parse(data, b.start, b.end) {
					if to, ok := relabel[e.typ]; ok {
						copy(data[e.start+4:e.start+8], to)
					}
					walk(e.body, e.end)
				}
			case "moov", "trak", "mdia", "minf", "stbl", "edts", "mvex", "dinf", "sinf", "schi":
				walk(b.body, b.end)
			}
		}
	}
	walk(0, len(data))
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "mvex", "dinf", "sinf", "schi", "stsd":
		return true
	}
	return false
}

// parse lists the boxes between start and end. A sample description entry
// (inside stsd) keeps its codec header before its child boxes, unlike every
// other container.
func parse(data []byte, start, end int) []box {
	if start < 0 || end > len(data) || start > end {
		return nil
	}
	var out []box
	i := start
	for i+8 <= end {
		size := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		body := i + 8
		if size == 1 {
			if i+16 > end {
				break
			}
			size = int(binary.BigEndian.Uint64(data[i+8:]))
			body = i + 16
		}
		if size < body-i || i+size > end {
			break
		}
		out = append(out, box{typ: typ, start: i, end: i + size, body: body})
		i += size
	}
	// stsd holds version/flags and an entry count before its entries.
	if len(out) == 1 && out[0].typ == "stsd" {
		entry := out[0].body + 8
		var entries []box
		for entry+8 <= out[0].end {
			size := int(binary.BigEndian.Uint32(data[entry:]))
			typ := string(data[entry+4 : entry+8])
			if size < 8 || entry+size > out[0].end {
				break
			}
			body := entry + 8 + 78 // visual sample entry fields
			if typ == "enca" {
				body = entry + 8 + 28 // audio sample entry fields
			}
			entries = append(entries, box{typ: typ, start: entry, end: entry + size, body: body})
			entry += size
		}
		return entries
	}
	return out
}

func findBox(data []byte, start, end int, typ string) (box, error) {
	for _, b := range parse(data, start, end) {
		if b.typ == typ {
			return b, nil
		}
	}
	return box{}, ErrMalformed
}

func children(data []byte, b box) []box { return parse(data, b.body, b.end) }

func findChild(data []byte, b box, typ string) (box, error) {
	return findBox(data, b.body, b.end, typ)
}
