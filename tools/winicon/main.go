// Command winicon writes the Windows resource objects that give lovkilde.exe its icon: one COFF
// object per architecture, holding an .rsrc section with the icon's images (RT_ICON) and the
// group that lists them (RT_GROUP_ICON). Go links a .syso file in a package's directory into
// the program when its name says the platform, so the objects live beside main.
//
//	go run ./tools/winicon cmd/lovkilde/lovkilde.ico cmd/lovkilde
//
// It is a generator rather than a build step, so a build needs nothing but the Go toolchain; run
// it again when the icon changes.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langEnUS    = 0x0409
)

// The architectures and the relocation each uses for an image-relative 32-bit address.
var arches = []struct {
	name    string
	machine uint16
	reloc   uint16
}{
	{"amd64", 0x8664, 3}, // IMAGE_REL_AMD64_ADDR32NB
	{"arm64", 0xaa64, 2}, // IMAGE_REL_ARM64_ADDR32NB
}

type image struct {
	entry [12]byte // the ICONDIRENTRY up to its offset: size, colours, planes, bit count, byte count
	data  []byte
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: winicon <icon.ico> <package directory>")
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	images, err := readICO(raw)
	if err != nil {
		log.Fatalf("%s: %v", os.Args[1], err)
	}
	for _, a := range arches {
		path := filepath.Join(os.Args[2], "rsrc_windows_"+a.name+".syso")
		if err := os.WriteFile(path, object(images, a.machine, a.reloc), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", path)
	}
}

// readICO splits an .ico file into its images.
func readICO(raw []byte) ([]image, error) {
	if len(raw) < 6 || binary.LittleEndian.Uint16(raw[0:]) != 0 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, errors.New("not an icon file")
	}
	n := int(binary.LittleEndian.Uint16(raw[4:]))
	if n == 0 || len(raw) < 6+16*n {
		return nil, errors.New("no images, or a truncated directory")
	}
	out := make([]image, n)
	for i := range out {
		e := raw[6+16*i : 6+16*(i+1)]
		size := binary.LittleEndian.Uint32(e[8:])
		at := binary.LittleEndian.Uint32(e[12:])
		if uint64(at)+uint64(size) > uint64(len(raw)) {
			return nil, fmt.Errorf("image %d lies past the end of the file", i)
		}
		copy(out[i].entry[:], e[:12])
		out[i].data = raw[at : at+size]
	}
	return out, nil
}

// object is the COFF object: file header, one section header, the section's resource tree and
// data, the relocations that make each data entry's address image-relative, and a symbol table
// holding the section's symbol, which the relocations name.
func object(images []image, machine, relocType uint16) []byte {
	sec, relocs := section(images)
	const headers = 20 + 40
	relocAt := headers + len(sec)
	symAt := relocAt + 10*len(relocs)

	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	// IMAGE_FILE_HEADER
	w(machine)
	w(uint16(1))     // sections
	w(uint32(0))     // time stamp: none, so the file is the same every time
	w(uint32(symAt)) // symbol table
	w(uint32(1))     // symbols
	w(uint16(0))     // optional header
	w(uint16(0))     // characteristics
	// IMAGE_SECTION_HEADER
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0))           // virtual size
	w(uint32(0))           // virtual address
	w(uint32(len(sec)))    // raw size
	w(uint32(headers))     // raw data
	w(uint32(relocAt))     // relocations
	w(uint32(0))           // line numbers
	w(uint16(len(relocs))) // relocation count
	w(uint16(0))           // line number count
	w(uint32(0x40000040))  // initialised data, readable
	b.Write(sec)
	for _, at := range relocs {
		w(uint32(at)) // the field to relocate
		w(uint32(0))  // symbol 0: the section
		w(relocType)
	}
	// The section's symbol, and an empty string table.
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0)) // value
	w(int16(1))  // section 1
	w(uint16(0)) // type
	w(uint8(3))  // IMAGE_SYM_CLASS_STATIC
	w(uint8(0))  // aux symbols
	w(uint32(4)) // string table size, itself only
	return b.Bytes()
}

// section is the .rsrc section — a three-level directory of type, id and language, then data
// entries, then the data — and where in it the data entries' addresses are, for relocation.
func section(images []image) ([]byte, []int) {
	type leaf struct {
		typ, id uint32
		data    []byte
	}
	var leaves []leaf
	for i, im := range images {
		leaves = append(leaves, leaf{rtIcon, uint32(i + 1), im.data})
	}
	var group bytes.Buffer
	_ = binary.Write(&group, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	for i, im := range images {
		group.Write(im.entry[:])
		_ = binary.Write(&group, binary.LittleEndian, uint16(i+1))
	}
	leaves = append(leaves, leaf{rtGroupIcon, 1, group.Bytes()})

	// Sizes, to place each part: a directory is 16 bytes and 8 per entry.
	types := []uint32{rtIcon, rtGroupIcon}
	idsOf := map[uint32][]int{} // leaf indexes per type, in id order
	for i, l := range leaves {
		idsOf[l.typ] = append(idsOf[l.typ], i)
	}
	root := 16 + 8*len(types)
	typeDirs := 0
	for _, t := range types {
		typeDirs += 16 + 8*len(idsOf[t])
	}
	langDirs := len(leaves) * (16 + 8)
	entriesAt := root + typeDirs + langDirs
	dataAt := entriesAt + 16*len(leaves)

	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	dir := func(n int) {
		w(uint32(0))
		w(uint32(0))
		w(uint16(0))
		w(uint16(0))
		w(uint16(0))
		w(uint16(n))
	}
	const sub = 0x80000000

	dir(len(types))
	at := root
	for _, t := range types {
		w(t)
		w(uint32(at) | sub)
		at += 16 + 8*len(idsOf[t])
	}
	langAt := root + typeDirs
	for _, t := range types {
		dir(len(idsOf[t]))
		for _, i := range idsOf[t] {
			w(leaves[i].id)
			w(uint32(langAt+24*i) | sub)
		}
	}
	for i := range leaves {
		dir(1)
		w(uint32(langEnUS))
		w(uint32(entriesAt + 16*i))
	}
	var relocs []int
	data := dataAt
	for _, l := range leaves {
		relocs = append(relocs, b.Len())
		w(uint32(data)) // made image-relative by the relocation
		w(uint32(len(l.data)))
		w(uint32(0)) // code page
		w(uint32(0))
		data += (len(l.data) + 7) &^ 7
	}
	for _, l := range leaves {
		b.Write(l.data)
		for b.Len()%8 != 0 {
			b.WriteByte(0)
		}
	}
	return b.Bytes(), relocs
}
